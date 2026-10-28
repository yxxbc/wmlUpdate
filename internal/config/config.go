// Package config loads and validates updater configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/yxxbc/wmlUpdate/internal/platform"
	"github.com/yxxbc/wmlUpdate/internal/sign"
	"github.com/yxxbc/wmlUpdate/internal/version"
)

const CurrentSchemaVersion = 1

// Config is the complete updater configuration. JSON is the only supported
// format so it can be generated and consumed by any language.
type Config struct {
	Schema         string         `json:"$schema,omitempty"`
	SchemaVersion  int            `json:"schema_version"`
	AppID          string         `json:"app_id"`
	Channel        string         `json:"channel,omitempty"`
	CurrentVersion string         `json:"current_version,omitempty"`
	Source         SourceConfig   `json:"source"`
	AssetRules     []AssetRule    `json:"asset_rules,omitempty"`
	Install        InstallConfig  `json:"install"`
	StateDir       string         `json:"state_dir,omitempty"`
	Security       SecurityConfig `json:"security,omitempty"`
	Download       DownloadConfig `json:"download,omitempty"`

	// File is the absolute path of the loaded configuration file.
	File string `json:"-"`
}

// SourceConfig describes where releases are discovered and downloaded.
type SourceConfig struct {
	Type string `json:"type"` // github | static | chain

	// Chain: sources are tried in order. A source that fails to return valid
	// metadata falls through to the next source.
	Sources []SourceConfig `json:"sources,omitempty"`

	// GitHub
	Owner             string `json:"owner,omitempty"`
	Repo              string `json:"repo,omitempty"`
	APIBase           string `json:"api_base,omitempty"`
	TagPrefix         string `json:"tag_prefix,omitempty"`
	TokenEnv          string `json:"token_env,omitempty"`
	IncludePrerelease bool   `json:"include_prerelease,omitempty"`

	// Static self-hosted manifest
	ManifestURL  string            `json:"manifest_url,omitempty"`
	ManifestURLs []string          `json:"manifest_urls,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`

	// Asset mirrors are URL templates tried when the primary asset download
	// fails. Placeholders: {url}, {owner}, {repo}, {tag}, {version}, {asset},
	// {name}, {os}, {arch}.
	AssetMirrors []string `json:"asset_mirrors,omitempty"`
}

// AssetRule selects a release asset for a platform. Rules are evaluated in
// order. Match supports {version}, {tag}, {os}, {arch} and {ext}, followed by
// standard path.Match wildcards.
type AssetRule struct {
	OS     string `json:"os,omitempty"`
	Arch   string `json:"arch,omitempty"`
	Match  string `json:"match,omitempty"`
	Format string `json:"format,omitempty"`
}

// InstallConfig controls how a verified artifact is installed.
type InstallConfig struct {
	Type            string            `json:"type"` // directory | file | command | external
	Path            string            `json:"path,omitempty"`
	Paths           map[string]string `json:"paths,omitempty"`
	StripComponents int               `json:"strip_components,omitempty"`
	Preserve        []string          `json:"preserve,omitempty"`
	FileMode        string            `json:"file_mode,omitempty"`
	KeepBackups     int               `json:"keep_backups,omitempty"`

	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	WorkDir string            `json:"workdir,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Timeout Duration          `json:"timeout,omitempty"`

	RelaunchPath string   `json:"relaunch_path,omitempty"`
	RelaunchArgs []string `json:"relaunch_args,omitempty"`

	MacOS MacOSConfig `json:"macos,omitempty"`
}

// MacOSConfig controls optional post-install metadata handling on macOS.
type MacOSConfig struct {
	// RemoveQuarantine removes com.apple.quarantine recursively from the target.
	// This bypasses Gatekeeper's first-launch check; enable only for updates
	// verified by this updater.
	RemoveQuarantine bool `json:"remove_quarantine,omitempty"`
	// AdHocSign force-signs the target with an ad-hoc signature. This can make
	// unsigned bundles launch, but destroys Developer ID signatures and
	// notarization; use only for unsigned distributions.
	AdHocSign bool `json:"ad_hoc_sign,omitempty"`
	// AllowSignerChange permits replacing a Developer ID-signed app with a
	// bundle signed by a different team. By default, when the installed app has
	// a TeamIdentifier, the staged app must verify and have the same team.
	AllowSignerChange bool `json:"allow_signer_change,omitempty"`
}

// SecurityConfig controls supply-chain verification and network boundaries.
type SecurityConfig struct {
	PublicKey               string   `json:"public_key,omitempty"`
	PublicKeyFile           string   `json:"public_key_file,omitempty"`
	ChecksumsAsset          string   `json:"checksums_asset,omitempty"`
	ChecksumsSignatureAsset string   `json:"checksums_signature_asset,omitempty"`
	AllowedHosts            []string `json:"allowed_hosts,omitempty"`
	AllowInsecureHTTP       bool     `json:"allow_insecure_http,omitempty"`
	AllowUnverifiedAssets   bool     `json:"allow_unverified_assets,omitempty"`
}

// DownloadConfig bounds downloads and retries.
type DownloadConfig struct {
	MaxBytes int64    `json:"max_bytes,omitempty"`
	Timeout  Duration `json:"timeout,omitempty"`
	Retries  int      `json:"retries,omitempty"`
	// Proxy overrides the standard environment proxy. Supports http, https,
	// socks5 and socks5h URLs.
	Proxy string `json:"proxy,omitempty"`
}

// Duration is a time.Duration encoded as a JSON string such as "15m".
type Duration time.Duration

func (d Duration) Value() time.Duration { return time.Duration(d) }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	if parsed <= 0 {
		return errors.New("duration must be positive")
	}
	*d = Duration(parsed)
	return nil
}

// Prepare validates a programmatically constructed configuration, applies
// defaults and resolves relative paths against baseDir. It is used by the
// public SDK; Load remains the JSON-file entry point.
func Prepare(cfg *Config, baseDir string) error {
	if cfg == nil {
		return errors.New("nil configuration")
	}
	if baseDir == "" {
		baseDir = "."
	}
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return fmt.Errorf("resolve base directory: %w", err)
	}
	if cfg.File == "" {
		cfg.File = filepath.Join(absBase, "updater.json")
	}
	return cfg.finalize()
}

// Load reads, expands and validates a JSON configuration file.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", abs, err)
	}
	if err := ensureEOF(dec); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", abs, err)
	}
	cfg.File = abs
	if err := cfg.finalize(); err != nil {
		return nil, fmt.Errorf("config %s: %w", abs, err)
	}
	return &cfg, nil
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}

var appIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (c *Config) finalize() error {
	if c.SchemaVersion == 0 {
		c.SchemaVersion = CurrentSchemaVersion
	}
	if c.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported schema_version %d (want %d)", c.SchemaVersion, CurrentSchemaVersion)
	}
	if !appIDPattern.MatchString(c.AppID) {
		return errors.New("app_id must match [A-Za-z0-9][A-Za-z0-9._-]{0,127}")
	}
	if c.Channel == "" {
		c.Channel = "stable"
	}
	c.Channel = strings.ToLower(c.Channel)
	if strings.ContainsAny(c.Channel, `/\`) || len(c.Channel) > 64 {
		return errors.New("channel must be a short name without path separators")
	}
	if c.CurrentVersion != "" {
		expanded, err := expandEnv(c.CurrentVersion)
		if err != nil {
			return fmt.Errorf("current_version: %w", err)
		}
		c.CurrentVersion = expanded
		if _, err := version.Parse(expanded); err != nil {
			return fmt.Errorf("current_version: %w", err)
		}
	}

	if err := c.finalizeSource(); err != nil {
		return err
	}
	if err := c.finalizeInstall(); err != nil {
		return err
	}
	if err := c.finalizeSecurity(); err != nil {
		return err
	}
	if err := c.finalizeDownload(); err != nil {
		return err
	}

	base := filepath.Dir(c.File)
	if c.StateDir == "" {
		c.StateDir = defaultStateDir(c.AppID)
	} else {
		p, err := expandPath(c.StateDir, base)
		if err != nil {
			return fmt.Errorf("state_dir: %w", err)
		}
		c.StateDir = p
	}
	return nil
}

func (c *Config) finalizeSource() error {
	if err := finalizeSourceConfig(&c.Source, "source", 0); err != nil {
		return err
	}

	seen := make(map[string]bool, len(c.AssetRules))
	for i := range c.AssetRules {
		r := &c.AssetRules[i]
		if r.OS != "" {
			normalized, err := platform.NormalizeOS(r.OS)
			if err != nil {
				return fmt.Errorf("asset_rules[%d].os: %w", i, err)
			}
			r.OS = normalized
		}
		if r.Arch != "" {
			normalized, err := platform.NormalizeArch(r.Arch)
			if err != nil {
				return fmt.Errorf("asset_rules[%d].arch: %w", i, err)
			}
			r.Arch = normalized
		}
		if r.OS == "" && r.Arch == "" && r.Match == "" {
			return fmt.Errorf("asset_rules[%d] must set at least one of os, arch or match", i)
		}
		if r.Format != "" {
			r.Format = normalizeFormat(r.Format)
			if !knownFormat(r.Format) {
				return fmt.Errorf("asset_rules[%d].format %q is not supported", i, r.Format)
			}
		}
		key := r.OS + "|" + r.Arch + "|" + r.Match
		if seen[key] {
			return fmt.Errorf("asset_rules[%d] duplicates an earlier rule", i)
		}
		seen[key] = true
	}
	return nil
}

func finalizeSourceConfig(sc *SourceConfig, path string, depth int) error {
	sc.Type = strings.ToLower(strings.TrimSpace(sc.Type))
	if depth > 1 {
		return fmt.Errorf("%s: nested source chains are not supported", path)
	}
	if len(sc.AssetMirrors) > 0 {
		mirrors := make([]string, 0, len(sc.AssetMirrors))
		for i, m := range sc.AssetMirrors {
			expanded, err := expandEnv(m)
			if err != nil {
				return fmt.Errorf("%s.asset_mirrors[%d]: %w", path, i, err)
			}
			if err := validateMirrorTemplate(expanded); err != nil {
				return fmt.Errorf("%s.asset_mirrors[%d]: %w", path, i, err)
			}
			mirrors = append(mirrors, expanded)
		}
		sc.AssetMirrors = mirrors
	}
	switch sc.Type {
	case "chain":
		if depth > 0 {
			return fmt.Errorf("%s: nested source chains are not supported", path)
		}
		if len(sc.Sources) == 0 {
			return fmt.Errorf("%s.sources must contain at least one source", path)
		}
		if sc.ManifestURL != "" || len(sc.ManifestURLs) > 0 || sc.Owner != "" || sc.Repo != "" {
			return fmt.Errorf("%s: chain sources must be configured inside sources[]", path)
		}
		for i := range sc.Sources {
			if err := finalizeSourceConfig(&sc.Sources[i], fmt.Sprintf("%s.sources[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
	case "github":
		if sc.Owner == "" || sc.Repo == "" {
			return fmt.Errorf("%s.owner and %s.repo are required for GitHub sources", path, path)
		}
		if sc.ManifestURL != "" || len(sc.ManifestURLs) > 0 {
			return fmt.Errorf("%s.manifest_url is only valid for static sources", path)
		}
		if sc.APIBase == "" {
			sc.APIBase = "https://api.github.com"
		}
		u, err := url.Parse(sc.APIBase)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("invalid %s.api_base %q", path, sc.APIBase)
		}
		sc.APIBase = strings.TrimRight(sc.APIBase, "/")
	case "static":
		urls := append([]string(nil), sc.ManifestURLs...)
		if sc.ManifestURL != "" {
			urls = append([]string{sc.ManifestURL}, urls...)
		}
		if len(urls) == 0 {
			return fmt.Errorf("%s.manifest_url or %s.manifest_urls is required for static sources", path, path)
		}
		seen := make(map[string]bool, len(urls))
		clean := make([]string, 0, len(urls))
		for i, raw := range urls {
			expanded, err := expandEnv(raw)
			if err != nil {
				return fmt.Errorf("%s.manifest_urls[%d]: %w", path, i, err)
			}
			u, err := url.Parse(expanded)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("invalid %s.manifest_url %q", path, expanded)
			}
			if seen[expanded] {
				continue
			}
			seen[expanded] = true
			clean = append(clean, expanded)
		}
		sc.ManifestURLs = clean
		sc.ManifestURL = clean[0]
		if len(sc.Headers) > 0 {
			headers := make(map[string]string, len(sc.Headers))
			for k, v := range sc.Headers {
				ev, err := expandEnv(v)
				if err != nil {
					return fmt.Errorf("%s.headers[%s]: %w", path, k, err)
				}
				headers[k] = ev
			}
			sc.Headers = headers
		}
	default:
		return fmt.Errorf("%s.type must be github, static or chain, got %q", path, sc.Type)
	}
	return nil
}

func validateMirrorTemplate(template string) error {
	if strings.TrimSpace(template) == "" {
		return errors.New("mirror template is empty")
	}
	sample := strings.NewReplacer(
		"{url}", "https://example.test/a",
		"{owner}", "owner",
		"{repo}", "repo",
		"{tag}", "v1.0.0",
		"{version}", "1.0.0",
		"{asset}", "update.zip",
		"{name}", "update.zip",
		"{os}", "linux",
		"{arch}", "amd64",
	).Replace(template)
	u, err := url.Parse(sample)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid URL template %q", template)
	}
	return nil
}

func (c *Config) finalizeInstall() error {
	base := filepath.Dir(c.File)
	c.Install.Type = strings.ToLower(strings.TrimSpace(c.Install.Type))
	if c.Install.Type == "" {
		c.Install.Type = "directory"
	}
	if c.Install.StripComponents < 0 {
		return errors.New("install.strip_components cannot be negative")
	}
	if c.Install.KeepBackups < 0 || c.Install.KeepBackups > 10 {
		return errors.New("install.keep_backups must be between 0 and 10")
	}
	if c.Install.KeepBackups == 0 && (c.Install.Type == "directory" || c.Install.Type == "file") {
		c.Install.KeepBackups = 1
	}
	if c.Install.FileMode != "" {
		mode, err := strconv.ParseUint(c.Install.FileMode, 8, 32)
		if err != nil || mode > 0o777 {
			return fmt.Errorf("install.file_mode must be an octal permission such as \"0755\"")
		}
		c.Install.FileMode = fmt.Sprintf("%04o", mode)
	}
	for _, pattern := range c.Install.Preserve {
		if pattern == "" || filepath.IsAbs(pattern) || strings.Contains(pattern, "..") {
			return fmt.Errorf("install.preserve pattern %q must be relative and must not contain parent-directory segments", pattern)
		}
		if !doublestar.ValidatePattern(filepath.ToSlash(pattern)) {
			return fmt.Errorf("install.preserve pattern %q is invalid", pattern)
		}
	}
	if c.Install.Paths != nil {
		normalized := make(map[string]string, len(c.Install.Paths))
		for key, value := range c.Install.Paths {
			normKey, err := normalizePathKey(key)
			if err != nil {
				return fmt.Errorf("install.paths[%s]: %w", key, err)
			}
			p, err := expandPath(value, base)
			if err != nil {
				return fmt.Errorf("install.paths[%s]: %w", key, err)
			}
			normalized[normKey] = p
		}
		c.Install.Paths = normalized
	}
	if c.Install.Path != "" {
		p, err := expandPath(c.Install.Path, base)
		if err != nil {
			return fmt.Errorf("install.path: %w", err)
		}
		c.Install.Path = p
	}
	if c.Install.WorkDir != "" {
		p, err := expandPath(c.Install.WorkDir, base)
		if err != nil {
			return fmt.Errorf("install.workdir: %w", err)
		}
		c.Install.WorkDir = p
	}
	if c.Install.RelaunchPath != "" {
		p, err := expandPath(c.Install.RelaunchPath, base)
		if err != nil {
			return fmt.Errorf("install.relaunch_path: %w", err)
		}
		c.Install.RelaunchPath = p
	}
	if c.Install.Env != nil {
		env := make(map[string]string, len(c.Install.Env))
		for k, v := range c.Install.Env {
			ev, err := expandEnv(v)
			if err != nil {
				return fmt.Errorf("install.env[%s]: %w", k, err)
			}
			env[k] = ev
		}
		c.Install.Env = env
	}
	switch c.Install.Type {
	case "directory":
		if c.Install.Path == "" && len(c.Install.Paths) == 0 {
			return errors.New("install.path or install.paths is required for directory installs")
		}
	case "file":
		if c.Install.Path == "" && len(c.Install.Paths) == 0 {
			return errors.New("install.path or install.paths is required for file installs")
		}
	case "command":
		if strings.TrimSpace(c.Install.Command) == "" {
			return errors.New("install.command is required for command installs")
		}
	case "external":
		// The integrator handles installation from the returned artifact path.
	default:
		return fmt.Errorf("install.type must be directory, file, command or external, got %q", c.Install.Type)
	}
	if c.Install.Timeout == 0 {
		c.Install.Timeout = Duration(30 * time.Minute)
	}
	if c.Install.Command != "" && strings.ContainsAny(c.Install.Command, "\r\n") {
		return errors.New("install.command must be a single executable path without newlines")
	}
	return nil
}

func (c *Config) finalizeSecurity() error {
	base := filepath.Dir(c.File)
	if c.Security.PublicKey != "" && c.Security.PublicKeyFile != "" {
		return errors.New("set only one of security.public_key and security.public_key_file")
	}
	if c.Security.PublicKeyFile != "" {
		p, err := expandPath(c.Security.PublicKeyFile, base)
		if err != nil {
			return fmt.Errorf("security.public_key_file: %w", err)
		}
		c.Security.PublicKeyFile = p
	}
	if c.Security.PublicKeyFile != "" || c.Security.PublicKey != "" {
		if _, err := sign.LoadPublicKey(c.Security.PublicKey, c.Security.PublicKeyFile); err != nil {
			return fmt.Errorf("security public key: %w", err)
		}
	}
	if c.Security.ChecksumsAsset == "" {
		c.Security.ChecksumsAsset = "checksums.txt"
	}
	if c.Security.ChecksumsSignatureAsset == "" {
		c.Security.ChecksumsSignatureAsset = c.Security.ChecksumsAsset + ".sig"
	}
	for _, h := range c.Security.AllowedHosts {
		if strings.Contains(h, "/") {
			return fmt.Errorf("security.allowed_hosts entry %q must be a host name, optionally with a port", h)
		}
	}
	return nil
}

func (c *Config) finalizeDownload() error {
	if c.Download.MaxBytes == 0 {
		c.Download.MaxBytes = 2 << 30 // 2 GiB
	}
	if c.Download.MaxBytes < 0 {
		return errors.New("download.max_bytes cannot be negative")
	}
	if c.Download.Timeout == 0 {
		c.Download.Timeout = Duration(30 * time.Minute)
	}
	if c.Download.Retries == 0 {
		c.Download.Retries = 3
	}
	if c.Download.Retries < 0 || c.Download.Retries > 10 {
		return errors.New("download.retries must be between 0 and 10")
	}
	if c.Download.Proxy != "" {
		expanded, err := expandEnv(c.Download.Proxy)
		if err != nil {
			return fmt.Errorf("download.proxy: %w", err)
		}
		u, err := url.Parse(expanded)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") || u.Host == "" {
			return errors.New("download.proxy must be an http, https, socks5 or socks5h URL")
		}
		c.Download.Proxy = expanded
	}
	return nil
}

// PublicKey returns the configured Ed25519 public key, or nil.
func (c *Config) PublicKey() ([]byte, error) {
	pub, err := sign.LoadPublicKey(c.Security.PublicKey, c.Security.PublicKeyFile)
	if err != nil {
		return nil, err
	}
	return pub, nil
}

// ResolveInstallPath selects the configured path for p.
func (c *Config) ResolveInstallPath(p platform.Platform) (string, error) {
	key := p.OS + "-" + p.Arch
	if v, ok := c.Install.Paths[key]; ok {
		return v, nil
	}
	if v, ok := c.Install.Paths[p.OS]; ok {
		return v, nil
	}
	if c.Install.Path != "" {
		return c.Install.Path, nil
	}
	return "", fmt.Errorf("no install path configured for %s", p)
}

// FileMode parses install.file_mode; zero means preserve existing permissions.
func (c *Config) FileMode() (os.FileMode, error) {
	if c.Install.FileMode == "" {
		return 0, nil
	}
	mode, err := strconv.ParseUint(c.Install.FileMode, 8, 32)
	if err != nil {
		return 0, err
	}
	return os.FileMode(mode), nil
}

func normalizePathKey(key string) (string, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return "", errors.New("path key is empty")
	}
	if strings.Contains(key, "-") {
		parts := strings.Split(key, "-")
		if len(parts) == 2 {
			osName, err := platform.NormalizeOS(parts[0])
			if err != nil {
				return "", err
			}
			arch, err := platform.NormalizeArch(parts[1])
			if err != nil {
				return "", err
			}
			return osName + "-" + arch, nil
		}
	}
	osName, err := platform.NormalizeOS(key)
	if err != nil {
		return "", err
	}
	return osName, nil
}

// expandEnv is like os.ExpandEnv but reports references to unset variables.
// Silent expansions to an empty string are a common source of broken updater
// configurations and leaked/failed authentication.
func expandEnv(s string) (string, error) {
	var missing []string
	expanded := os.Expand(s, func(key string) string {
		v, ok := os.LookupEnv(key)
		if !ok {
			missing = append(missing, key)
			return ""
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("environment variable(s) not set: %s", strings.Join(missing, ", "))
	}
	return expanded, nil
}

func expandPath(s, baseDir string) (string, error) {
	expanded, err := expandEnv(s)
	if err != nil {
		return "", err
	}
	if expanded == "~" || strings.HasPrefix(expanded, "~/") || strings.HasPrefix(expanded, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		expanded = filepath.Join(home, expanded[1:])
	}
	if !filepath.IsAbs(expanded) {
		expanded = filepath.Join(baseDir, expanded)
	}
	return filepath.Clean(expanded), nil
}

func defaultStateDir(appID string) string {
	switch runtime.GOOS {
	case "windows":
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			return filepath.Join(dir, "wml-update", appID)
		}
		if dir, err := os.UserConfigDir(); err == nil {
			return filepath.Join(dir, "wml-update", appID)
		}
	case "darwin":
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, "Library", "Application Support", "wml-update", appID)
		}
	default:
		if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
			return filepath.Join(xdg, "wml-update", appID)
		}
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, ".local", "state", "wml-update", appID)
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "wml-update", appID)
	}
	return filepath.Join(os.TempDir(), "wml-update", appID)
}

func normalizeFormat(s string) string {
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), ".")) {
	case "tgz", "tar.gz":
		return "tar.gz"
	case "tar":
		return "tar"
	case "zip":
		return "zip"
	case "gz", "gzip":
		return "gz"
	case "raw", "bin", "exe", "msi", "pkg", "dmg":
		return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "."))
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

func knownFormat(s string) bool {
	switch s {
	case "", "zip", "tar.gz", "tar", "gz", "raw", "bin", "exe", "msi", "pkg", "dmg":
		return true
	default:
		return false
	}
}
