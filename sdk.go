// Package wmlupdate is a lightweight, embeddable updater library.
//
// The public surface is deliberately small:
//
//	u, err := wmlupdate.New(wmlupdate.Options{
//	    ConfigPath:     "updater.json",
//	    CurrentVersion: currentVersion,
//	})
//	update, err := u.Check(ctx)
//	if update.Available {
//	    result, err := u.Apply(ctx, update, wmlupdate.ApplyOptions{})
//	}
//
// The library owns release discovery, verified download, installation, backup
// and rollback bookkeeping. It does not start a daemon, require a specific
// application framework, or control the host process lifecycle. For languages
// without a native binding, the command in cmd/wml-update exposes the same
// engine over NDJSON and an optional loopback HTTP API.
package wmlupdate

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/yxxbc/wmlUpdate/internal/config"
	"github.com/yxxbc/wmlUpdate/internal/engine"
	"github.com/yxxbc/wmlUpdate/internal/event"
)

// Options configures an Updater.
type Options struct {
	// ConfigPath loads a complete updater.json configuration, including
	// environment references and $schema support.
	ConfigPath string
	// Config supplies a programmatic configuration. Set exactly one of
	// ConfigPath and Config.
	Config *Config
	// BaseDir resolves relative paths in a programmatic Config. It defaults to
	// the current working directory.
	BaseDir string
	// CurrentVersion is the running application version. When empty, the
	// library falls back to the recorded state and then Config.CurrentVersion.
	CurrentVersion string
	// Platform overrides the target platform for cross-platform packaging tests.
	Platform *Platform
	// Events receives progress, log and warning callbacks. It is invoked
	// synchronously and should return quickly.
	Events EventFunc
}

// Updater is a configured, embeddable updater. Check may be called
// concurrently; Apply takes an inter-process lock and is serialized.
type Updater struct {
	engine  *engine.Engine
	mu      sync.RWMutex
	current string
}

// New constructs an Updater.
func New(opts Options) (*Updater, error) {
	if opts.ConfigPath != "" && opts.Config != nil {
		return nil, usageErr("set exactly one of Options.ConfigPath and Options.Config")
	}
	if opts.ConfigPath == "" && opts.Config == nil {
		return nil, usageErr("Options.ConfigPath or Options.Config is required")
	}
	var cfg *config.Config
	var err error
	if opts.ConfigPath != "" {
		cfg, err = config.Load(opts.ConfigPath)
		if err != nil {
			return nil, wrapError("config", err)
		}
	} else {
		baseDir := opts.BaseDir
		if baseDir == "" {
			baseDir, err = os.Getwd()
			if err != nil {
				return nil, wrapError("io", err)
			}
		}
		cfg, err = opts.Config.internal(baseDir)
		if err != nil {
			return nil, wrapError("config", err)
		}
	}
	p, err := opts.Platform.internal()
	if err != nil {
		return nil, wrapError("config", err)
	}
	sink := event.Sink(event.NopSink{})
	if opts.Events != nil {
		sink = callbackSink(opts.Events)
	}
	eng, err := engine.New(cfg, p, sink)
	if err != nil {
		return nil, wrapError(engine.CodeOf(err), err)
	}
	return &Updater{engine: eng, current: opts.CurrentVersion}, nil
}

// Close releases idle network connections. It is safe to call more than once.
func (u *Updater) Close() {
	if u != nil && u.engine != nil {
		u.engine.Close()
	}
}

// Update is the result of Check. Its exported fields are informational. Apply
// uses an opaque checked snapshot retained by the Updater; do not construct or
// serialize Update values.
type Update struct {
	Available      bool      `json:"available"`
	CurrentVersion string    `json:"current_version"`
	Version        string    `json:"version"`
	Channel        string    `json:"channel,omitempty"`
	Source         string    `json:"source,omitempty"`
	Tag            string    `json:"tag,omitempty"`
	Notes          string    `json:"notes,omitempty"`
	PublishedAt    time.Time `json:"published_at,omitempty"`
	Mandatory      bool      `json:"mandatory,omitempty"`
	Prerelease     bool      `json:"prerelease,omitempty"`
	Asset          Asset     `json:"asset"`

	owner   *Updater
	checked *engine.Check
}

// Asset describes the selected platform artifact.
type Asset struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Format string `json:"format,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

// Check discovers the latest eligible release without downloading it.
func (u *Updater) Check(ctx context.Context) (*Update, error) {
	u.mu.RLock()
	current := u.current
	u.mu.RUnlock()
	chk, err := u.engine.CheckDetailed(ctx, current)
	if err != nil {
		return nil, publicError(err)
	}
	return u.publicUpdate(chk), nil
}

// ApplyOptions controls installation.
type ApplyOptions struct {
	// Defer stages the update and applies it from a detached helper after the
	// selected processes exit. Recommended on Windows when the target is
	// running, and required when replacing the running updater itself.
	Defer bool `json:"defer,omitempty"`
	// WaitPIDs lists processes that must exit before a deferred apply.
	WaitPIDs []int `json:"wait_pids,omitempty"`
}

// Apply status values.
const (
	StatusUpdated        = "updated"
	StatusDownloaded     = "downloaded"
	StatusApplyScheduled = "apply_scheduled"
)

// Result describes a completed Apply.
type Result struct {
	Status          string `json:"status"`
	PreviousVersion string `json:"previous_version,omitempty"`
	Version         string `json:"version,omitempty"`
	ArtifactPath    string `json:"artifact_path,omitempty"`
	PlanID          string `json:"plan_id,omitempty"`
	HelperPID       int    `json:"helper_pid,omitempty"`
}

// Apply installs a release returned by Check. The exact checked release is
// retained internally, so source metadata is not re-fetched; artifact bytes are
// still downloaded and verified against the checked SHA-256 before use.
func (u *Updater) Apply(ctx context.Context, update *Update, opts ApplyOptions) (*Result, error) {
	if update == nil {
		return nil, &Error{Code: "no_update", Err: ErrNoUpdate}
	}
	if update.owner != u {
		return nil, usageErr("update was produced by a different Updater")
	}
	if update.checked == nil || update.checked.Result == nil {
		return nil, usageErr("update is not a checked snapshot; call Check first and do not serialize Update values")
	}
	if update.checked.Result.Status != engine.StatusUpdateAvailable {
		return nil, &Error{Code: "no_update", Err: ErrNoUpdate}
	}
	if !opts.Defer && len(opts.WaitPIDs) > 0 {
		return nil, usageErr("WaitPIDs requires Defer")
	}
	res, err := u.engine.ApplyChecked(ctx, update.checked, engine.UpdateOptions{
		Defer:    opts.Defer,
		WaitPIDs: opts.WaitPIDs,
	})
	if err != nil {
		return nil, publicError(err)
	}
	previous := res.PreviousVersion
	if previous == "" {
		previous = res.CurrentVersion
	}
	out := &Result{
		Status:          res.Status,
		PreviousVersion: previous,
		Version:         res.LatestVersion,
		ArtifactPath:    res.ArtifactPath,
		PlanID:          res.PlanID,
		HelperPID:       res.HelperPID,
	}
	if out.Status == engine.StatusUpdated && out.Version != "" {
		u.mu.Lock()
		u.current = out.Version
		u.mu.Unlock()
	}
	return out, nil
}

// ErrNoUpdate is returned when Apply is called without an available update.
var ErrNoUpdate = errors.New("no update available")

// Error is a stable, machine-readable SDK error.
type Error struct {
	Code string
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Code returns the stable error code for err, or "internal" for unknown errors.
func Code(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return engine.CodeOf(err)
}

func (u *Updater) publicUpdate(chk *engine.Check) *Update {
	r := chk.Result
	out := &Update{
		Available:      r.Status == engine.StatusUpdateAvailable,
		CurrentVersion: r.CurrentVersion,
		Version:        r.LatestVersion,
		Channel:        r.Channel,
		Source:         r.Source,
		Tag:            r.Tag,
		Notes:          r.Notes,
		Mandatory:      r.Mandatory,
		Prerelease:     r.Prerelease,
		owner:          u,
		checked:        chk,
	}
	if r.PublishedAt != nil {
		out.PublishedAt = *r.PublishedAt
	}
	if r.Asset != nil {
		out.Asset = Asset{
			Name: r.Asset.Name, OS: r.Asset.OS, Arch: r.Asset.Arch,
			Format: r.Asset.Format, SHA256: r.Asset.SHA256, Size: r.Asset.Size,
		}
	}
	return out
}

type callbackSink EventFunc

func (s callbackSink) Emit(e event.Event) {
	if s == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	s(Event{
		Type:    EventType(e.Type),
		Time:    e.Time,
		Phase:   e.Phase,
		Message: e.Message,
		Bytes:   e.Bytes,
		Total:   e.Total,
		Percent: e.Percent,
	})
}

func publicError(err error) error {
	if err == nil {
		return nil
	}
	var sdkErr *Error
	if errors.As(err, &sdkErr) {
		return err
	}
	return &Error{Code: engine.CodeOf(err), Err: err}
}

func wrapError(code string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Err: err}
}

func usageErr(message string) error {
	return &Error{Code: "config", Err: errors.New(message)}
}
