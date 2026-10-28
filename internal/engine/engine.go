// Package engine orchestrates checks, verified downloads, installation,
// rollback and the JSON-facing result model.
package engine

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/yxxbc/wmlUpdate/internal/buildinfo"
	"github.com/yxxbc/wmlUpdate/internal/checksum"
	"github.com/yxxbc/wmlUpdate/internal/config"
	"github.com/yxxbc/wmlUpdate/internal/event"
	"github.com/yxxbc/wmlUpdate/internal/httpx"
	"github.com/yxxbc/wmlUpdate/internal/install"
	"github.com/yxxbc/wmlUpdate/internal/lock"
	"github.com/yxxbc/wmlUpdate/internal/platform"
	"github.com/yxxbc/wmlUpdate/internal/release"
	"github.com/yxxbc/wmlUpdate/internal/source"
	"github.com/yxxbc/wmlUpdate/internal/state"
	"github.com/yxxbc/wmlUpdate/internal/version"
)

// Status values in Result.
const (
	StatusUpdateAvailable = "update_available"
	StatusUpToDate        = "up_to_date"
	StatusUpdated         = "updated"
	StatusDownloaded      = "downloaded"
	StatusApplyScheduled  = "apply_scheduled"
	StatusRolledBack      = "rolled_back"
)

// Error is a typed updater failure with a stable machine-readable code.
type Error struct {
	Code string
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the stable error code, or "internal".
func CodeOf(err error) string {
	var ue *Error
	if errors.As(err, &ue) {
		return ue.Code
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "cancelled"
	}
	return "internal"
}

func fail(code string, format string, args ...any) error {
	return &Error{Code: code, Err: fmt.Errorf(format, args...)}
}

func wrap(code string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Err: err}
}

// AssetInfo is the public, serializable description of a selected artifact.
type AssetInfo struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Format string `json:"format,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

// Result is returned by check, update, fetch, rollback and mark operations.
type Result struct {
	Status          string     `json:"status"`
	CurrentVersion  string     `json:"current_version,omitempty"`
	PreviousVersion string     `json:"previous_version,omitempty"`
	LatestVersion   string     `json:"latest_version,omitempty"`
	Channel         string     `json:"channel,omitempty"`
	Source          string     `json:"source,omitempty"`
	Tag             string     `json:"tag,omitempty"`
	PublishedAt     *time.Time `json:"published_at,omitempty"`
	Notes           string     `json:"notes,omitempty"`
	Mandatory       bool       `json:"mandatory,omitempty"`
	Prerelease      bool       `json:"prerelease,omitempty"`
	Asset           *AssetInfo `json:"asset,omitempty"`
	ArtifactPath    string     `json:"artifact_path,omitempty"`
	PlanID          string     `json:"plan_id,omitempty"`
	HelperPID       int        `json:"helper_pid,omitempty"`
}

// UpdateOptions controls an update.
type UpdateOptions struct {
	CurrentVersion string
	Force          bool
	Defer          bool
	WaitPIDs       []int
}

// FetchOptions controls a fetch.
type FetchOptions struct {
	CurrentVersion string
	Output         string
}

// Engine wires a configuration to sources, state and an event sink.
type Engine struct {
	cfg      *config.Config
	platform platform.Platform
	sink     event.Sink
	store    *state.Store
	provider source.Provider
	client   *http.Client
	lockWait time.Duration
}

// New constructs an engine.
func New(cfg *config.Config, p platform.Platform, sink event.Sink) (*Engine, error) {
	if sink == nil {
		sink = event.NopSink{}
	}
	store, err := state.Open(cfg.StateDir)
	if err != nil {
		return nil, wrap("state", err)
	}
	cacheDir, err := store.Subdir("cache")
	if err != nil {
		cacheDir = ""
	}
	client := httpx.NewClient(cfg.Download.Timeout.Value(), httpx.Policy{
		UserAgent:     buildinfo.UserAgent(),
		AllowInsecure: cfg.Security.AllowInsecureHTTP,
		AllowedHosts:  cfg.Security.AllowedHosts,
		Proxy:         cfg.Download.Proxy,
	})
	provider, err := source.New(cfg, client, cacheDir)
	if err != nil {
		return nil, wrap("config", err)
	}
	return &Engine{
		cfg:      cfg,
		platform: p,
		sink:     sink,
		store:    store,
		provider: provider,
		client:   client,
		lockWait: 2 * time.Minute,
	}, nil
}

// Close releases idle HTTP connections.
func (e *Engine) Close() { e.client.CloseIdleConnections() }

// Check is an internal release candidate with the resolved asset. It is
// exported only inside the module so the public SDK can apply a checked release
// without re-fetching mutable source metadata.
type Check struct {
	Result  *Result
	Current version.Version
	Release *release.Release
	Asset   release.Asset
}

// Check reports whether a newer release exists without downloading it.
func (e *Engine) Check(ctx context.Context, currentVersion string) (*Result, error) {
	chk, err := e.CheckDetailed(ctx, currentVersion)
	if err != nil {
		return nil, err
	}
	return chk.Result, nil
}

// CheckDetailed resolves the current version, fetches the latest release and
// selects a platform asset.
func (e *Engine) CheckDetailed(ctx context.Context, currentVersion string) (*Check, error) {
	st, err := e.store.Load(e.cfg.AppID)
	if err != nil {
		return nil, wrap("state", err)
	}
	current, err := e.resolveCurrent(currentVersion, st)
	if err != nil {
		return nil, err
	}
	return e.checkWith(ctx, current)
}

func (e *Engine) checkWith(ctx context.Context, current version.Version) (*Check, error) {
	e.emitLog("source", "querying %s source", e.provider.Name())
	rel, err := e.provider.Fetch(ctx)
	if err != nil {
		if errors.Is(err, source.ErrNoRelease) {
			return nil, wrap("no_release", err)
		}
		return nil, wrap("source", err)
	}
	asset, err := selectAsset(rel, e.cfg, e.platform)
	if err != nil {
		return nil, wrap("no_asset", err)
	}
	if asset.SHA256 == "" && !e.cfg.Security.AllowUnverifiedAssets {
		return nil, fail("integrity", "release %s has no SHA-256 for %s and security.allow_unverified_assets is false", rel.Tag, asset.Name)
	}
	if asset.SHA256 != "" && !validSHA256(asset.SHA256) {
		return nil, fail("integrity", "release %s has an invalid SHA-256 for %s", rel.Tag, asset.Name)
	}
	if err := e.validateAsset(asset); err != nil {
		return nil, err
	}
	res := &Result{
		CurrentVersion: current.String(),
		LatestVersion:  rel.Version.String(),
		Channel:        rel.Channel,
		Source:         rel.Source,
		Tag:            rel.Tag,
		Notes:          truncate(rel.Notes, 256<<10),
		Mandatory:      rel.Mandatory,
		Prerelease:     rel.Prerelease,
		Asset: &AssetInfo{
			Name:   asset.Name,
			OS:     asset.OS,
			Arch:   asset.Arch,
			Format: asset.Format,
			SHA256: strings.ToLower(asset.SHA256),
			Size:   asset.Size,
		},
	}
	if !rel.PublishedAt.IsZero() {
		t := rel.PublishedAt.UTC()
		res.PublishedAt = &t
	}
	if rel.Version.Compare(current) > 0 {
		res.Status = StatusUpdateAvailable
	} else {
		res.Status = StatusUpToDate
	}
	return &Check{Result: res, Current: current, Release: rel, Asset: asset}, nil
}

// Update checks, downloads, verifies and installs the latest release.
func (e *Engine) Update(ctx context.Context, opts UpdateOptions) (*Result, error) {
	if opts.Defer && e.cfg.Install.Type == "external" {
		return nil, fail("config", "--defer cannot be used with install.type external")
	}
	lk, err := e.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer lk.Release()

	st, mgr, err := e.loadForWrite()
	if err != nil {
		return nil, err
	}
	if err := e.recover(mgr); err != nil {
		return nil, err
	}
	current, err := e.resolveCurrent(opts.CurrentVersion, st)
	if err != nil {
		return nil, err
	}
	chk, err := e.checkWith(ctx, current)
	if err != nil {
		return nil, err
	}
	if opts.Force {
		chk.Result.Status = StatusUpdateAvailable
	}
	if chk.Result.Status == StatusUpToDate {
		e.recordCheck(st)
		_ = e.store.Save(st)
		return chk.Result, nil
	}
	return e.installChecked(ctx, st, mgr, chk, opts)
}

// ApplyChecked installs a release previously returned by CheckDetailed. It does
// not re-fetch metadata: the source release and selected asset are retained in
// the Check value and the artifact is still downloaded and verified.
func (e *Engine) ApplyChecked(ctx context.Context, chk *Check, opts UpdateOptions) (*Result, error) {
	if chk == nil || chk.Result == nil || chk.Release == nil {
		return nil, fail("no_update", "no checked update was supplied")
	}
	if chk.Result.Status != StatusUpdateAvailable {
		return nil, fail("no_update", "release %s is not newer than the current version", chk.Result.LatestVersion)
	}
	if opts.Defer && e.cfg.Install.Type == "external" {
		return nil, fail("config", "--defer cannot be used with install.type external")
	}
	lk, err := e.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer lk.Release()
	st, mgr, err := e.loadForWrite()
	if err != nil {
		return nil, err
	}
	if err := e.recover(mgr); err != nil {
		return nil, err
	}
	return e.installChecked(ctx, st, mgr, chk, opts)
}

func (e *Engine) installChecked(ctx context.Context, st *state.State, mgr *install.Manager, chk *Check, opts UpdateOptions) (*Result, error) {
	if opts.Defer && e.cfg.Install.Type == "external" {
		return nil, fail("config", "--defer cannot be used with install.type external")
	}
	current := chk.Current
	rel := chk.Release
	asset := chk.Asset
	res := cloneResult(chk.Result)

	artifactPath, err := e.download(ctx, st, rel, asset)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, wrap("cancelled", err)
	}
	res.ArtifactPath = artifactPath
	if sha, _, hashErr := checksum.File(artifactPath); hashErr == nil {
		res.Asset.SHA256 = sha
	}
	if e.cfg.Install.Type == "external" {
		st.LastCheck = time.Now().UTC()
		st.Channel = e.cfg.Channel
		_ = e.store.Save(st)
		res.Status = StatusDownloaded
		res.CurrentVersion = current.String()
		return res, nil
	}

	plan, err := install.Prepare(e.cfg, e.platform, asset, artifactPath, rel.Version.String(), current.String(), e.sink)
	if err != nil {
		return nil, wrap("install", err)
	}
	if err := ctx.Err(); err != nil {
		cleanupPlan(plan)
		return nil, wrap("cancelled", err)
	}
	if opts.Defer {
		pid, err := e.schedule(ctx, plan, opts.WaitPIDs)
		if err != nil {
			cleanupPlan(plan)
			return nil, wrap("install", err)
		}
		st.LastCheck = time.Now().UTC()
		st.Channel = e.cfg.Channel
		_ = e.store.Save(st)
		res.Status = StatusApplyScheduled
		res.PlanID = plan.ID
		res.HelperPID = pid
		return res, nil
	}

	st.LastCheck = time.Now().UTC()
	st.Channel = e.cfg.Channel
	if err := mgr.Apply(ctx, plan); err != nil {
		cleanupPlan(plan)
		return nil, wrap("install", err)
	}
	res.Status = StatusUpdated
	res.PreviousVersion = current.String()
	res.CurrentVersion = rel.Version.String()
	return res, nil
}

// Fetch downloads and verifies an artifact without installing it.
func (e *Engine) Fetch(ctx context.Context, opts FetchOptions) (*Result, error) {
	lk, err := e.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer lk.Release()
	st, err := e.store.Load(e.cfg.AppID)
	if err != nil {
		return nil, wrap("state", err)
	}
	current, err := e.resolveCurrent(opts.CurrentVersion, st)
	if err != nil {
		return nil, err
	}
	chk, err := e.checkWith(ctx, current)
	if err != nil {
		return nil, err
	}
	res := chk.Result
	if res.Status == StatusUpToDate {
		return res, nil
	}
	artifactPath, err := e.download(ctx, st, chk.Release, chk.Asset)
	if err != nil {
		return nil, err
	}
	if sha, _, hashErr := checksum.File(artifactPath); hashErr == nil {
		res.Asset.SHA256 = sha
	}
	if opts.Output != "" {
		absOutput, err := filepath.Abs(opts.Output)
		if err != nil {
			return nil, wrap("io", err)
		}
		if err := copyOutput(artifactPath, absOutput); err != nil {
			return nil, wrap("io", err)
		}
		res.ArtifactPath = absOutput
	} else {
		res.ArtifactPath = artifactPath
	}
	res.Status = StatusDownloaded
	e.recordCheck(st)
	_ = e.store.Save(st)
	return res, nil
}

// Rollback restores the latest retained backup.
func (e *Engine) Rollback(ctx context.Context) (*Result, error) {
	lk, err := e.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer lk.Release()
	st, mgr, err := e.loadForWrite()
	if err != nil {
		return nil, err
	}
	if err := e.recover(mgr); err != nil {
		return nil, err
	}
	previous := st.InstalledVersion
	backup, err := mgr.Rollback(ctx)
	if err != nil {
		return nil, wrap("rollback", err)
	}
	return &Result{
		Status:          StatusRolledBack,
		PreviousVersion: previous,
		CurrentVersion:  backup.Version,
		Channel:         e.cfg.Channel,
	}, nil
}

// MarkInstalled records a version installed outside the built-in installer.
func (e *Engine) MarkInstalled(ctx context.Context, versionString, digest string) (*Result, error) {
	lk, err := e.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer lk.Release()
	v, err := version.Parse(strings.TrimSpace(versionString))
	if err != nil {
		return nil, wrap("config", err)
	}
	digest = strings.ToLower(strings.TrimSpace(digest))
	if digest != "" && !validSHA256(digest) {
		return nil, fail("config", "sha256 must be 64 hexadecimal characters")
	}
	_, mgr, err := e.loadForWrite()
	if err != nil {
		return nil, err
	}
	if err := e.recover(mgr); err != nil {
		return nil, err
	}
	st, err := e.store.Load(e.cfg.AppID)
	if err != nil {
		return nil, wrap("state", err)
	}
	previous := st.InstalledVersion
	st.InstalledVersion = v.String()
	st.Channel = e.cfg.Channel
	st.LastUpdate = &state.Record{
		Version:         v.String(),
		PreviousVersion: previous,
		InstalledAt:     time.Now().UTC(),
		Strategy:        "external",
		SHA256:          strings.ToLower(strings.TrimSpace(digest)),
	}
	if err := e.store.Save(st); err != nil {
		return nil, wrap("state", err)
	}
	return &Result{
		Status:          "marked",
		PreviousVersion: previous,
		CurrentVersion:  v.String(),
		Channel:         e.cfg.Channel,
	}, nil
}

// Status returns persisted updater state for diagnostics.
func (e *Engine) Status() (*state.State, error) {
	st, err := e.store.Load(e.cfg.AppID)
	if err != nil {
		return nil, wrap("state", err)
	}
	return st, nil
}

func (e *Engine) acquire(ctx context.Context) (*lock.Lock, error) {
	lk, err := lock.Acquire(ctx, e.cfg.StateDir, e.lockWait)
	if err != nil {
		return nil, wrap("locked", err)
	}
	return lk, nil
}

func (e *Engine) loadForWrite() (*state.State, *install.Manager, error) {
	st, err := e.store.Load(e.cfg.AppID)
	if err != nil {
		return nil, nil, wrap("state", err)
	}
	return st, &install.Manager{
		Store:       e.store,
		Data:        st,
		KeepBackups: e.cfg.Install.KeepBackups,
		Sink:        e.sink,
	}, nil
}

func (e *Engine) recover(mgr *install.Manager) error {
	warnings, err := mgr.Recover()
	for _, w := range warnings {
		e.sink.Emit(event.Event{Type: event.TypeWarning, Phase: "recover", Message: w})
	}
	if err != nil {
		return wrap("state", err)
	}
	return nil
}

func (e *Engine) resolveCurrent(explicit string, st *state.State) (version.Version, error) {
	candidates := []string{explicit, st.InstalledVersion, e.cfg.CurrentVersion}
	var raw string
	for _, c := range candidates {
		if strings.TrimSpace(c) != "" {
			raw = c
			break
		}
	}
	if raw == "" {
		return version.Version{}, fail("config", "current version is unknown; pass --current-version or set current_version")
	}
	v, err := version.Parse(raw)
	if err != nil {
		return version.Version{}, wrap("config", err)
	}
	return v, nil
}

func (e *Engine) download(ctx context.Context, st *state.State, rel *release.Release, asset release.Asset) (string, error) {
	if asset.SHA256 != "" && !validSHA256(asset.SHA256) {
		return "", fail("integrity", "invalid SHA-256 for %s", asset.Name)
	}
	downloads, err := e.store.Subdir("downloads")
	if err != nil {
		return "", wrap("state", err)
	}
	base := sanitizeFilename(path.Base(asset.Name))
	if base == "" {
		base = "artifact"
	}
	key := rel.Version.String() + "__" + base
	if asset.SHA256 != "" {
		key = strings.ToLower(asset.SHA256) + "__" + base
	}
	dest := filepath.Join(downloads, key)

	if asset.SHA256 != "" {
		if _, err := os.Stat(dest); err == nil {
			if digest, _, err := checksum.File(dest); err == nil && strings.EqualFold(digest, asset.SHA256) {
				e.emitLog("download", "using cached verified artifact")
				return dest, nil
			}
			_ = os.Remove(dest)
		}
	} else {
		// Unverified artifacts are never reused: there is no digest with which
		// to decide whether an existing file is the intended download.
		_ = os.Remove(dest)
	}

	verified := asset.SHA256 != ""
	if !verified {
		e.sink.Emit(event.Event{
			Type:    event.TypeWarning,
			Phase:   "download",
			Message: "artifact has no SHA-256; security.allow_unverified_assets is enabled",
		})
	}
	e.emitProgress("download", 0, asset.Size)

	seen := make(map[string]bool)
	var lastErr error
	var downloaded int64
	tryAsset := func(a release.Asset) bool {
		urls := append([]string{a.URL}, a.FallbackURLs...)
		primaryHost := hostOfURL(a.URL)
		for _, rawURL := range urls {
			if rawURL == "" || seen[rawURL] {
				continue
			}
			seen[rawURL] = true
			headers := a.Headers
			if hostOfURL(rawURL) != primaryHost {
				// Never forward a source token to a mirror on another host.
				headers = nil
			}
			_ = os.Remove(dest + ".part")
			n, err := httpx.Download(ctx, e.client, httpx.DownloadOptions{
				URL:             rawURL,
				Headers:         headers,
				Dest:            dest,
				ExpectedSHA256:  a.SHA256,
				ExpectedSize:    a.Size,
				MaxBytes:        e.cfg.Download.MaxBytes,
				Retries:         e.cfg.Download.Retries,
				AllowUnverified: !verified,
				Progress: func(current, total int64) {
					e.emitProgress("download", current, total)
				},
			})
			if err == nil {
				downloaded = n
				return true
			}
			lastErr = err
			_ = os.Remove(dest + ".part")
			if ctx.Err() != nil {
				return false
			}
			e.sink.Emit(event.Event{
				Type:    event.TypeWarning,
				Phase:   "download",
				Message: "download attempt failed; trying the next mirror",
			})
		}
		return false
	}

	if tryAsset(asset) {
		return e.finishDownload(dest, downloaded, verified)
	}
	if err := ctx.Err(); err != nil {
		return "", wrap("cancelled", err)
	}

	// A source chain can provide the same version from another mirror after
	// metadata succeeded but the artifact host is unreachable. The fallback
	// must have the same SHA-256 as the checked release.
	if fp, ok := e.provider.(source.FallbackProvider); ok && verified {
		e.emitLog("download", "primary artifact failed; checking fallback sources")
		fallbacks, fallbackErr := fp.FallbackReleases(ctx, rel.Version)
		for _, fallbackRelease := range fallbacks {
			fallbackAsset, err := selectAsset(fallbackRelease, e.cfg, e.platform)
			if err != nil || !strings.EqualFold(fallbackAsset.SHA256, asset.SHA256) {
				continue
			}
			if tryAsset(fallbackAsset) {
				return e.finishDownload(dest, downloaded, verified)
			}
		}
		if fallbackErr != nil && lastErr == nil {
			lastErr = fallbackErr
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no download URL succeeded")
	}
	code := "download"
	var se *httpx.StatusError
	switch {
	case errors.Is(lastErr, httpx.ErrChecksum), errors.Is(lastErr, io.ErrUnexpectedEOF):
		code = "integrity"
	case errors.As(lastErr, &se):
		code = "network"
	}
	return "", wrap(code, lastErr)
}

func (e *Engine) finishDownload(dest string, n int64, verified bool) (string, error) {
	if !verified {
		digest, size, err := checksum.File(dest)
		if err != nil {
			return "", wrap("integrity", err)
		}
		e.sink.Emit(event.Event{
			Type:    event.TypeWarning,
			Phase:   "verify",
			Message: fmt.Sprintf("unverified artifact downloaded: sha256=%s size=%d", digest, size),
		})
	} else {
		e.emitLog("verify", "SHA-256 verified (%d bytes)", n)
	}
	return dest, nil
}

func hostOfURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

func (e *Engine) recordCheck(st *state.State) {
	st.LastCheck = time.Now().UTC()
	st.Channel = e.cfg.Channel
}

func (e *Engine) emitLog(phase, format string, args ...any) {
	e.sink.Emit(event.Event{Type: event.TypeLog, Phase: phase, Message: fmt.Sprintf(format, args...)})
}

func (e *Engine) emitProgress(phase string, current, total int64) {
	pct := float64(0)
	if total > 0 {
		pct = float64(current) / float64(total) * 100
		if pct > 100 {
			pct = 100
		}
	}
	e.sink.Emit(event.Event{Type: event.TypeProgress, Phase: phase, Bytes: current, Total: total, Percent: pct})
}

func (e *Engine) validateAsset(asset release.Asset) error {
	if e.cfg.Install.Type != "directory" {
		return nil
	}
	switch asset.Format {
	case "zip", "tar.gz", "tar":
		return nil
	default:
		return fail("no_asset", "directory installs require a zip, tar.gz or tar artifact; asset %s has format %q", asset.Name, asset.Format)
	}
}

func cloneResult(r *Result) *Result {
	if r == nil {
		return nil
	}
	c := *r
	if r.Asset != nil {
		a := *r.Asset
		c.Asset = &a
	}
	if r.PublishedAt != nil {
		t := *r.PublishedAt
		c.PublishedAt = &t
	}
	return &c
}

func validSHA256(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n\n[truncated]"
}

func sanitizeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 100 {
		ext := filepath.Ext(out)
		out = out[:100-len(ext)] + ext
	}
	return strings.Trim(out, ".")
}

func copyOutput(src, dst string) error {
	if dst == "" {
		return errors.New("output path is empty")
	}
	abs, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".wml-fetch-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, in); err != nil {
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
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	return os.Rename(tmpName, abs)
}

func cleanupPlan(plan *install.Plan) {
	if plan == nil {
		return
	}
	if plan.Stage != "" {
		_ = os.RemoveAll(plan.Stage)
	}
	// Never delete plan.Backup here: a failed apply may need it for recovery.
	_ = os.Remove(install.PlanPath(plan.StateDir, plan.ID))
}
