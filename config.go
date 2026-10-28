package wmlupdate

import (
	"errors"
	"fmt"
	"time"

	"github.com/yxxbc/wmlUpdate/internal/config"
	"github.com/yxxbc/wmlUpdate/internal/platform"
)

// Config is the programmatic form of the updater configuration. It mirrors the
// updater.json schema but is intentionally limited to the fields an embedding
// application needs. Use Options.ConfigPath when you want to ship a JSON file
// with environment expansion and schema support.
type Config struct {
	AppID          string         `json:"app_id"`
	Channel        string         `json:"channel,omitempty"`
	CurrentVersion string         `json:"current_version,omitempty"`
	Source         SourceConfig   `json:"source"`
	AssetRules     []AssetRule    `json:"asset_rules,omitempty"`
	Install        InstallConfig  `json:"install"`
	StateDir       string         `json:"state_dir,omitempty"`
	Security       SecurityConfig `json:"security,omitempty"`
	Download       DownloadConfig `json:"download,omitempty"`
}

// Source type values.
const (
	SourceGitHub = "github"
	SourceStatic = "static"
)

// SourceConfig describes GitHub Releases or a self-hosted manifest.
type SourceConfig struct {
	Type string `json:"type"`

	// GitHub Releases.
	Owner             string `json:"owner,omitempty"`
	Repo              string `json:"repo,omitempty"`
	APIBase           string `json:"api_base,omitempty"`
	TagPrefix         string `json:"tag_prefix,omitempty"`
	TokenEnv          string `json:"token_env,omitempty"`
	IncludePrerelease bool   `json:"include_prerelease,omitempty"`

	// Self-hosted static manifest.
	ManifestURL string            `json:"manifest_url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

// AssetRule selects a release asset for a platform. Rules are evaluated in
// order. Match supports {version}, {tag}, {os}, {arch} and glob wildcards.
type AssetRule struct {
	OS     string `json:"os,omitempty"`
	Arch   string `json:"arch,omitempty"`
	Match  string `json:"match,omitempty"`
	Format string `json:"format,omitempty"`
}

// Install type values.
const (
	InstallDirectory = "directory"
	InstallFile      = "file"
	InstallCommand   = "command"
	InstallExternal  = "external"
)

// InstallConfig controls how a verified artifact is applied.
type InstallConfig struct {
	Type            string            `json:"type"`
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
	Timeout time.Duration     `json:"timeout,omitempty"`

	RelaunchPath string   `json:"relaunch_path,omitempty"`
	RelaunchArgs []string `json:"relaunch_args,omitempty"`
}

// SecurityConfig controls signature verification and network boundaries.
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
	MaxBytes int64         `json:"max_bytes,omitempty"`
	Timeout  time.Duration `json:"timeout,omitempty"`
	Retries  int           `json:"retries,omitempty"`
}

// Platform overrides the target platform. The zero value means the host
// platform.
type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

func (c *Config) internal(baseDir string) (*config.Config, error) {
	if c == nil {
		return nil, errors.New("nil configuration")
	}
	if c.Install.Timeout < 0 || c.Download.Timeout < 0 {
		return nil, errors.New("timeouts cannot be negative")
	}
	cfg := &config.Config{
		SchemaVersion:  config.CurrentSchemaVersion,
		AppID:          c.AppID,
		Channel:        c.Channel,
		CurrentVersion: c.CurrentVersion,
		StateDir:       c.StateDir,
		Source: config.SourceConfig{
			Type:              c.Source.Type,
			Owner:             c.Source.Owner,
			Repo:              c.Source.Repo,
			APIBase:           c.Source.APIBase,
			TagPrefix:         c.Source.TagPrefix,
			TokenEnv:          c.Source.TokenEnv,
			IncludePrerelease: c.Source.IncludePrerelease,
			ManifestURL:       c.Source.ManifestURL,
			Headers:           cloneMap(c.Source.Headers),
		},
		Install: config.InstallConfig{
			Type:            c.Install.Type,
			Path:            c.Install.Path,
			Paths:           cloneMap(c.Install.Paths),
			StripComponents: c.Install.StripComponents,
			Preserve:        cloneSlice(c.Install.Preserve),
			FileMode:        c.Install.FileMode,
			KeepBackups:     c.Install.KeepBackups,
			Command:         c.Install.Command,
			Args:            cloneSlice(c.Install.Args),
			WorkDir:         c.Install.WorkDir,
			Env:             cloneMap(c.Install.Env),
			Timeout:         config.Duration(c.Install.Timeout),
			RelaunchPath:    c.Install.RelaunchPath,
			RelaunchArgs:    cloneSlice(c.Install.RelaunchArgs),
		},
		Security: config.SecurityConfig{
			PublicKey:               c.Security.PublicKey,
			PublicKeyFile:           c.Security.PublicKeyFile,
			ChecksumsAsset:          c.Security.ChecksumsAsset,
			ChecksumsSignatureAsset: c.Security.ChecksumsSignatureAsset,
			AllowedHosts:            cloneSlice(c.Security.AllowedHosts),
			AllowInsecureHTTP:       c.Security.AllowInsecureHTTP,
			AllowUnverifiedAssets:   c.Security.AllowUnverifiedAssets,
		},
		Download: config.DownloadConfig{
			MaxBytes: c.Download.MaxBytes,
			Timeout:  config.Duration(c.Download.Timeout),
			Retries:  c.Download.Retries,
		},
	}
	for _, r := range c.AssetRules {
		cfg.AssetRules = append(cfg.AssetRules, config.AssetRule{
			OS: r.OS, Arch: r.Arch, Match: r.Match, Format: r.Format,
		})
	}
	if err := config.Prepare(cfg, baseDir); err != nil {
		return nil, err
	}
	return cfg, nil
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneSlice[T any](s []T) []T {
	if s == nil {
		return nil
	}
	return append([]T(nil), s...)
}

func (p *Platform) internal() (platform.Platform, error) {
	if p == nil || (p.OS == "" && p.Arch == "") {
		return platform.Current(), nil
	}
	osName := p.OS
	arch := p.Arch
	if osName == "" || arch == "" {
		return platform.Platform{}, fmt.Errorf("platform override requires both os and arch")
	}
	return platform.Normalize(osName, arch)
}
