// Package install applies verified artifacts transactionally. Directory and
// file installs create an on-disk journal, keep a backup and support rollback.
package install

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/yxxbc/wmlUpdate/internal/archive"
	"github.com/yxxbc/wmlUpdate/internal/config"
	"github.com/yxxbc/wmlUpdate/internal/event"
	"github.com/yxxbc/wmlUpdate/internal/platform"
	"github.com/yxxbc/wmlUpdate/internal/release"
	"github.com/yxxbc/wmlUpdate/internal/state"
)

const planSchemaVersion = 1

// Plan is the serializable unit of work applied either in-process or by a
// detached helper after the application exits.
type Plan struct {
	SchemaVersion  int               `json:"schema_version"`
	ID             string            `json:"id"`
	AppID          string            `json:"app_id"`
	CurrentVersion string            `json:"current_version,omitempty"`
	Version        string            `json:"version"`
	Strategy       string            `json:"strategy"`
	Target         string            `json:"target,omitempty"`
	ArtifactPath   string            `json:"artifact_path,omitempty"`
	Stage          string            `json:"stage,omitempty"`
	Backup         string            `json:"backup,omitempty"`
	FileMode       string            `json:"file_mode,omitempty"`
	KeepBackups    int               `json:"keep_backups"`
	StateDir       string            `json:"state_dir"`
	RelaunchPath   string            `json:"relaunch_path,omitempty"`
	RelaunchArgs   []string          `json:"relaunch_args,omitempty"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	WorkDir        string            `json:"workdir,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	CommandTimeout int               `json:"command_timeout_seconds,omitempty"`
}

// Manager applies plans against one state store.
type Manager struct {
	Store       *state.Store
	Data        *state.State
	KeepBackups int
	Sink        event.Sink
}

func (m *Manager) warn(format string, args ...any) {
	if m.Sink != nil {
		m.Sink.Emit(event.Event{Type: event.TypeWarning, Message: fmt.Sprintf(format, args...)})
	}
}

// Prepare stages an artifact for installation. It performs extraction, prefix
// stripping and preservation before any live files are touched.
func Prepare(cfg *config.Config, p platform.Platform, artifact release.Asset, artifactPath, versionString, currentVersion string, sink event.Sink) (*Plan, error) {
	if cfg.Install.Type == "external" {
		return nil, errors.New("install.type external does not prepare a plan")
	}
	target, err := cfg.ResolveInstallPath(p)
	if cfg.Install.Type == "command" && err != nil {
		target = ""
	} else if err != nil {
		return nil, err
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	plan := &Plan{
		SchemaVersion:  planSchemaVersion,
		ID:             id,
		AppID:          cfg.AppID,
		CurrentVersion: currentVersion,
		Version:        versionString,
		Strategy:       cfg.Install.Type,
		Target:         target,
		ArtifactPath:   artifactPath,
		KeepBackups:    cfg.Install.KeepBackups,
		StateDir:       cfg.StateDir,
		RelaunchPath:   cfg.Install.RelaunchPath,
		RelaunchArgs:   append([]string(nil), cfg.Install.RelaunchArgs...),
		Command:        cfg.Install.Command,
		Args:           append([]string(nil), cfg.Install.Args...),
		WorkDir:        cfg.Install.WorkDir,
		Env:            cfg.Install.Env,
		CommandTimeout: int(cfg.Install.Timeout.Value().Seconds()),
	}
	if plan.Version == "" {
		return nil, errors.New("artifact version is empty")
	}

	switch plan.Strategy {
	case "directory":
		if err := plan.prepareDirectory(cfg, artifact, artifactPath, sink); err != nil {
			return nil, err
		}
	case "file":
		if err := plan.prepareFile(cfg); err != nil {
			return nil, err
		}
	case "command":
		if plan.Target != "" {
			if err := ensureSafeTarget(plan.Target, plan.StateDir); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("unsupported install strategy %q", plan.Strategy)
	}
	return plan, nil
}

func (p *Plan) prepareDirectory(cfg *config.Config, artifact release.Asset, artifactPath string, sink event.Sink) error {
	if err := ensureSafeTarget(p.Target, p.StateDir); err != nil {
		return err
	}
	if info, err := os.Stat(p.Target); err == nil && !info.IsDir() {
		return fmt.Errorf("directory install target %s is not a directory", p.Target)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(p.Target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create install parent: %w", err)
	}
	stage, err := os.MkdirTemp(parent, ".wml-update-stage-")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(stage)
		}
	}()
	p.Stage = stage
	p.Backup = filepath.Join(parent, fmt.Sprintf(".wml-update-backup-%s-%s", sanitizeName(p.AppID), p.ID))
	format := artifact.Format
	if format == "" {
		format = archive.DetectFormat(artifactPath)
	}
	if format != "zip" && format != "tar.gz" && format != "tar" {
		return fmt.Errorf("directory installs require a zip, tar.gz or tar artifact, got %q", format)
	}
	if err := archive.Extract(artifactPath, stage, format, archive.ExtractOptions{
		StripComponents: cfg.Install.StripComponents,
		MaxBytes:        cfg.Download.MaxBytes,
		AllowSymlinks:   true,
	}); err != nil {
		return fmt.Errorf("extract artifact: %w", err)
	}
	if len(cfg.Install.Preserve) > 0 {
		if _, err := os.Stat(p.Target); err == nil {
			if err := preservePaths(p.Target, stage, cfg.Install.Preserve); err != nil {
				return fmt.Errorf("preserve existing files: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	mode := os.FileMode(0o755)
	if info, err := os.Stat(p.Target); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.Chmod(stage, mode); err != nil {
		return fmt.Errorf("set staging directory mode: %w", err)
	}
	cleanup = false
	if sink != nil {
		sink.Emit(event.Event{Type: event.TypeLog, Phase: "install", Message: "artifact staged"})
	}
	return nil
}

func (p *Plan) prepareFile(cfg *config.Config) error {
	if err := ensureSafeTarget(p.Target, p.StateDir); err != nil {
		return err
	}
	if info, err := os.Stat(p.Target); err == nil && info.IsDir() {
		return fmt.Errorf("file install target %s is a directory", p.Target)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(p.Target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create install parent: %w", err)
	}
	p.Backup = filepath.Join(parent, fmt.Sprintf(".wml-update-backup-%s-%s", sanitizeName(p.AppID), p.ID))
	stage := filepath.Join(parent, fmt.Sprintf(".wml-update-stage-%s-%s", sanitizeName(p.AppID), p.ID))
	p.Stage = stage

	mode := os.FileMode(0o755)
	if info, err := os.Stat(p.Target); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if cfg.Install.FileMode != "" {
		parsed, err := cfg.FileMode()
		if err != nil {
			return err
		}
		mode = parsed
	}
	src, err := os.Open(p.ArtifactPath)
	if err != nil {
		return fmt.Errorf("open artifact: %w", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create staged file: %w", err)
	}
	_, copyErr := io.Copy(dst, src)
	if copyErr == nil {
		copyErr = dst.Sync()
	}
	closeErr := dst.Close()
	if copyErr != nil {
		_ = os.Remove(stage)
		return fmt.Errorf("stage artifact: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(stage)
		return closeErr
	}
	if err := os.Chmod(stage, mode); err != nil {
		_ = os.Remove(stage)
		return err
	}
	return nil
}

func ensureSafeTarget(target, stateDir string) error {
	if target == "" {
		return errors.New("install target is empty")
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	if abs == filepath.VolumeName(abs)+string(filepath.Separator) || abs == "/" {
		return fmt.Errorf("refusing to install into filesystem root %s", abs)
	}
	home, _ := os.UserHomeDir()
	if home != "" && abs == filepath.Clean(home) {
		return errors.New("refusing to install into the user home directory")
	}
	if stateDir != "" {
		stateAbs, _ := filepath.Abs(stateDir)
		if withinPath(abs, stateAbs) {
			return fmt.Errorf("install target %s is inside the updater state directory", abs)
		}
		if withinPath(stateAbs, abs) {
			return fmt.Errorf("updater state directory is inside install target %s", abs)
		}
	}
	if info, err := os.Lstat(abs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("install target %s is a symlink; configure the real directory", abs)
	}
	return nil
}

func withinPath(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func preservePaths(target, stage string, patterns []string) error {
	return filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == target {
			return nil
		}
		rel, err := filepath.Rel(target, p)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		matched := false
		for _, pattern := range patterns {
			if ok, _ := doublestar.Match(filepath.ToSlash(pattern), relSlash); ok {
				matched = true
				break
			}
		}
		if !matched {
			return nil
		}
		if err := copyPreserved(p, filepath.Join(stage, rel), d.IsDir()); err != nil {
			return err
		}
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
}

func copyPreserved(src, dst string, isDir bool) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		_ = os.RemoveAll(dst)
		return os.Symlink(link, dst)
	case isDir:
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		return copyTree(src, dst)
	case info.Mode().IsRegular():
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		return copyFile(src, dst, info.Mode().Perm())
	default:
		return fmt.Errorf("cannot preserve unsupported file type %s", src)
	}
}

func copyTree(src, dst string) error {
	dirModes := make(map[string]os.FileMode)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			dirModes[target] = info.Mode().Perm()
			return nil
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyFile(p, target, info.Mode().Perm())
		default:
			return fmt.Errorf("cannot copy unsupported file type %s", p)
		}
	})
	if err != nil {
		return err
	}
	// Apply restrictive directory modes after all children exist.
	for dir, mode := range dirModes {
		if err := os.Chmod(dir, mode.Perm()&0o755); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(dst, mode.Perm()&0o755)
}

// Apply performs the live installation. The caller must hold the app lock.
func (m *Manager) Apply(ctx context.Context, plan *Plan) error {
	if err := validatePlan(plan); err != nil {
		return err
	}
	if m.Data == nil {
		return errors.New("install manager has no state data")
	}
	if m.Data.Pending != nil {
		return errors.New("another install transaction is pending; run recovery first")
	}
	m.Data.Pending = &state.Pending{
		ID:       plan.ID,
		Target:   plan.Target,
		Stage:    plan.Stage,
		Backup:   plan.Backup,
		Version:  plan.Version,
		Previous: plan.CurrentVersion,
		Strategy: plan.Strategy,
		Phase:    "prepared",
		Started:  time.Now().UTC(),
	}
	if err := m.Store.Save(m.Data); err != nil {
		m.Data.Pending = nil
		return fmt.Errorf("write install journal: %w", err)
	}

	committed := false
	defer func() {
		if !committed && (plan.Strategy == "directory" || plan.Strategy == "file") {
			m.restorePending()
		}
	}()

	switch plan.Strategy {
	case "directory", "file":
		if err := m.applyReplace(plan); err != nil {
			return err
		}
	case "command":
		if err := m.applyCommand(ctx, plan); err != nil {
			m.Data.Pending.Phase = "failed"
			_ = m.Store.Save(m.Data)
			return err
		}
		m.Data.Pending.Phase = "committed"
		if err := m.Store.Save(m.Data); err != nil {
			return fmt.Errorf("record command completion: %w", err)
		}
	default:
		return fmt.Errorf("unsupported strategy %q", plan.Strategy)
	}

	committed = true
	m.complete(plan)
	return nil
}

func (m *Manager) applyReplace(plan *Plan) error {
	if _, err := os.Lstat(plan.Stage); err != nil {
		return fmt.Errorf("staged artifact is missing: %w", err)
	}
	_, targetErr := os.Lstat(plan.Target)
	targetExists := targetErr == nil
	if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
		return targetErr
	}
	if targetExists {
		if _, err := os.Lstat(plan.Backup); err == nil {
			return fmt.Errorf("backup path already exists: %s", plan.Backup)
		}
		if err := os.Rename(plan.Target, plan.Backup); err != nil {
			return fmt.Errorf("move existing installation to backup: %w (on Windows, close the application and use --defer)", err)
		}
		m.Data.Pending.Phase = "backed_up"
		if err := m.Store.Save(m.Data); err != nil {
			_ = os.Rename(plan.Backup, plan.Target)
			return fmt.Errorf("write install journal: %w", err)
		}
	}
	if err := os.Rename(plan.Stage, plan.Target); err != nil {
		if targetExists {
			_ = os.Rename(plan.Backup, plan.Target)
		}
		return fmt.Errorf("activate staged installation: %w", err)
	}
	m.Data.Pending.Phase = "committed"
	if err := m.Store.Save(m.Data); err != nil {
		return fmt.Errorf("record committed installation: %w", err)
	}
	return nil
}

func (m *Manager) applyCommand(ctx context.Context, plan *Plan) error {
	command := expandPlaceholders(plan.Command, plan)
	if command == "" {
		return errors.New("install command is empty")
	}
	args := make([]string, len(plan.Args))
	for i, a := range plan.Args {
		args[i] = expandPlaceholders(a, plan)
	}
	timeout := time.Duration(plan.CommandTimeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, command, args...)
	cmd.Dir = plan.WorkDir
	cmd.Env = os.Environ()
	for k, v := range plan.Env {
		cmd.Env = append(cmd.Env, k+"="+expandPlaceholders(v, plan))
	}
	out := &limitedBuffer{limit: 1 << 20}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		if runCtx.Err() != nil {
			return fmt.Errorf("installer timed out after %s: %w", timeout, runCtx.Err())
		}
		msg := strings.TrimSpace(out.String())
		if msg != "" {
			return fmt.Errorf("installer failed: %w: %s", err, msg)
		}
		return fmt.Errorf("installer failed: %w", err)
	}
	if m.Sink != nil {
		if msg := strings.TrimSpace(out.String()); msg != "" {
			m.Sink.Emit(event.Event{Type: event.TypeLog, Phase: "install", Message: msg})
		}
	}
	return nil
}

func (m *Manager) complete(plan *Plan) {
	prev := plan.CurrentVersion
	m.Data.InstalledVersion = plan.Version
	m.Data.LastUpdate = &state.Record{
		Version:         plan.Version,
		PreviousVersion: prev,
		InstalledAt:     time.Now().UTC(),
		Strategy:        plan.Strategy,
	}
	if plan.Backup != "" {
		if _, err := os.Stat(plan.Backup); err == nil {
			m.Data.Backups = append(m.Data.Backups, state.Backup{
				ID:        plan.ID,
				Path:      plan.Backup,
				Target:    plan.Target,
				Version:   prev,
				Strategy:  plan.Strategy,
				CreatedAt: time.Now().UTC(),
			})
		}
	}
	// Drop the journal before pruning, so a failed cleanup cannot leave the
	// state pointing at an inconsistent transaction.
	m.Data.Pending = nil
	if err := m.Store.Save(m.Data); err != nil {
		m.warn("installation applied but state could not be saved: %v", err)
		return
	}
	m.trimBackups(plan.KeepBackups)
	if err := m.Store.Save(m.Data); err != nil {
		m.warn("state backup pruning could not be saved: %v", err)
	}
	_ = os.RemoveAll(plan.Stage)
	_ = os.Remove(PlanPath(plan.StateDir, plan.ID))
	m.launch(plan)
}

// PlanPath returns the conventional helper plan path for a transaction.
func PlanPath(stateDir, id string) string {
	return filepath.Join(stateDir, "plans", id+".json")
}

func (m *Manager) trimBackups(keep int) {
	if keep < 0 {
		keep = 0
	}
	if len(m.Data.Backups) <= keep {
		return
	}
	remove := m.Data.Backups[:len(m.Data.Backups)-keep]
	m.Data.Backups = append([]state.Backup(nil), m.Data.Backups[len(m.Data.Backups)-keep:]...)
	for _, b := range remove {
		if b.Path != "" {
			_ = os.RemoveAll(b.Path)
		}
	}
}

func (m *Manager) restorePending() {
	p := m.Data.Pending
	if p == nil {
		return
	}
	switch p.Phase {
	case "backed_up":
		if _, err := os.Stat(p.Backup); err == nil {
			if _, err := os.Lstat(p.Target); err == nil {
				_ = os.RemoveAll(p.Target)
			}
			_ = os.Rename(p.Backup, p.Target)
		}
	}
	if p.Stage != "" {
		_ = os.RemoveAll(p.Stage)
	}
	m.Data.Pending = nil
	_ = m.Store.Save(m.Data)
}

func (m *Manager) launch(plan *Plan) {
	if plan.RelaunchPath == "" {
		return
	}
	path := expandPlaceholders(plan.RelaunchPath, plan)
	args := make([]string, len(plan.RelaunchArgs))
	for i, a := range plan.RelaunchArgs {
		args[i] = expandPlaceholders(a, plan)
	}
	cmd := exec.Command(path, args...)
	if plan.Target != "" {
		cmd.Dir = plan.Target
	}
	cmd.Stdout, cmd.Stderr, cmd.Stdin = nil, nil, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		m.warn("update installed but relaunch failed: %v", err)
		return
	}
	_ = cmd.Process.Release()
}

func expandPlaceholders(s string, plan *Plan) string {
	r := strings.NewReplacer(
		"{artifact}", plan.ArtifactPath,
		"{install_dir}", plan.Target,
		"{target}", plan.Target,
		"{version}", plan.Version,
		"{previous_version}", plan.CurrentVersion,
		"{state_dir}", plan.StateDir,
	)
	return r.Replace(s)
}

func validatePlan(plan *Plan) error {
	if plan == nil {
		return errors.New("nil install plan")
	}
	if plan.SchemaVersion != planSchemaVersion {
		return fmt.Errorf("unsupported plan schema %d", plan.SchemaVersion)
	}
	switch plan.Strategy {
	case "directory", "file":
		if plan.Target == "" || plan.Stage == "" {
			return errors.New("plan is missing target or stage")
		}
		if !filepath.IsAbs(plan.Target) || !filepath.IsAbs(plan.Stage) {
			return errors.New("plan target and stage must be absolute")
		}
		if filepath.Dir(plan.Stage) != filepath.Dir(plan.Target) {
			return errors.New("stage and target must be on the same filesystem and parent")
		}
		if plan.Backup != "" && filepath.Dir(plan.Backup) != filepath.Dir(plan.Target) {
			return errors.New("backup must be a sibling of the target")
		}
		if err := ensureSafeTarget(plan.Target, plan.StateDir); err != nil {
			return err
		}
	case "command":
		if plan.Command == "" {
			return errors.New("plan is missing install command")
		}
	default:
		return fmt.Errorf("unsupported plan strategy %q", plan.Strategy)
	}
	return nil
}

// Rollback restores the most recent retained backup.
func (m *Manager) Rollback(ctx context.Context) (*state.Backup, error) {
	if m.Data.Pending != nil {
		return nil, errors.New("cannot roll back while an install transaction is pending")
	}
	if len(m.Data.Backups) == 0 {
		return nil, errors.New("no backup is available to roll back")
	}
	b := m.Data.Backups[len(m.Data.Backups)-1]
	if b.Strategy != "directory" && b.Strategy != "file" {
		return nil, fmt.Errorf("strategy %q cannot be rolled back", b.Strategy)
	}
	if _, err := os.Lstat(b.Path); err != nil {
		return nil, fmt.Errorf("backup is missing: %w", err)
	}
	if err := ensureSafeTarget(b.Target, m.Store.Dir); err != nil {
		return nil, err
	}
	failed := b.Path + ".failed-" + b.ID
	_, targetErr := os.Lstat(b.Target)
	targetExists := targetErr == nil
	if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
		return nil, targetErr
	}
	if targetExists {
		if err := os.Rename(b.Target, failed); err != nil {
			return nil, fmt.Errorf("move current installation aside: %w", err)
		}
	}
	if err := os.Rename(b.Path, b.Target); err != nil {
		if targetExists {
			_ = os.Rename(failed, b.Target)
		}
		return nil, fmt.Errorf("restore backup: %w", err)
	}
	m.Data.InstalledVersion = b.Version
	m.Data.LastUpdate = &state.Record{
		Version:     b.Version,
		InstalledAt: time.Now().UTC(),
		Strategy:    "rollback",
	}
	m.Data.Backups = m.Data.Backups[:len(m.Data.Backups)-1]
	if err := m.Store.Save(m.Data); err != nil {
		return nil, fmt.Errorf("save rollback state: %w", err)
	}
	if targetExists {
		_ = os.RemoveAll(failed)
	}
	_ = ctx
	return &b, nil
}

// Recover repairs or completes an interrupted installation. It must be called
// while holding the app lock and returns human-readable warnings.
func (m *Manager) Recover() ([]string, error) {
	p := m.Data.Pending
	if p == nil {
		return nil, nil
	}
	var warnings []string
	switch p.Strategy {
	case "directory", "file":
		backupExists := p.Backup != "" && pathExists(p.Backup)
		targetExists := p.Target != "" && pathExists(p.Target)
		stageExists := p.Stage != "" && pathExists(p.Stage)
		switch {
		case p.Phase == "committed" || (p.Phase == "backed_up" && targetExists && !stageExists):
			m.Data.InstalledVersion = p.Version
			m.Data.LastUpdate = &state.Record{
				Version:         p.Version,
				PreviousVersion: p.Previous,
				InstalledAt:     time.Now().UTC(),
				Strategy:        p.Strategy,
			}
			if backupExists {
				m.Data.Backups = append(m.Data.Backups, state.Backup{
					ID: p.ID, Path: p.Backup, Target: p.Target, Version: p.Previous, Strategy: p.Strategy, CreatedAt: time.Now().UTC(),
				})
			}
			warnings = append(warnings, "completed an interrupted installation and kept the previous version for rollback")
		case backupExists:
			if targetExists {
				if err := os.RemoveAll(p.Target); err != nil {
					return nil, fmt.Errorf("recover: remove partial installation: %w", err)
				}
			}
			if err := os.Rename(p.Backup, p.Target); err != nil {
				return nil, fmt.Errorf("recover: restore backup: %w", err)
			}
			warnings = append(warnings, "restored the previous version after an interrupted installation")
		default:
			warnings = append(warnings, "cleared an incomplete installation with no backup")
		}
		if stageExists {
			_ = os.RemoveAll(p.Stage)
		}
	case "command":
		warnings = append(warnings, "cleared an interrupted command installer journal; verify the application manually")
	}
	m.Data.Pending = nil
	if err := m.Store.Save(m.Data); err != nil {
		return warnings, err
	}
	return warnings, nil
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// WritePlan atomically writes a plan for the detached helper.
func WritePlan(path string, plan *Plan) error {
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".plan-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// ReadPlan reads a helper plan.
func ReadPlan(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var plan Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, err
	}
	if err := validatePlan(&plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

func randomID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := b.String()
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

type limitedBuffer struct {
	limit int
	data  []byte
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		if len(p) < remaining {
			remaining = len(p)
		}
		b.data = append(b.data, p[:remaining]...)
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return string(b.data) }
