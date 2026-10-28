// Package publish builds self-hosted manifests, signed checksum files and
// release archives from a publish specification.
package publish

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/yxxbc/wmlUpdate/internal/archive"
	"github.com/yxxbc/wmlUpdate/internal/checksum"
	"github.com/yxxbc/wmlUpdate/internal/manifest"
	"github.com/yxxbc/wmlUpdate/internal/platform"
	"github.com/yxxbc/wmlUpdate/internal/sign"
	"github.com/yxxbc/wmlUpdate/internal/version"
)

// Spec describes a release to publish.
type Spec struct {
	AppID       string      `json:"app_id"`
	Version     string      `json:"version"`
	Channel     string      `json:"channel"`
	PublishedAt string      `json:"published_at,omitempty"`
	Notes       string      `json:"notes,omitempty"`
	NotesFile   string      `json:"notes_file,omitempty"`
	BaseURL     string      `json:"base_url,omitempty"`
	Assets      []AssetSpec `json:"assets"`
}

// AssetSpec points at a prebuilt artifact, or at a directory to archive.
type AssetSpec struct {
	Path   string `json:"path,omitempty"`
	Dir    string `json:"dir,omitempty"`
	URL    string `json:"url,omitempty"`
	Name   string `json:"name,omitempty"`
	OS     string `json:"os,omitempty"`
	Arch   string `json:"arch,omitempty"`
	Format string `json:"format,omitempty"`
}

// Options controls publish.
type Options struct {
	OutDir       string
	SigningKey   string
	BaseURL      string
	ManifestName string
}

// Result lists produced files.
type Result struct {
	ManifestPath  string   `json:"manifest_path"`
	SignaturePath string   `json:"signature_path,omitempty"`
	ChecksumsPath string   `json:"checksums_path"`
	Assets        []string `json:"assets"`
}

// Publish executes a spec.
func Publish(specPath string, opts Options) (*Result, error) {
	absSpec, err := filepath.Abs(specPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(absSpec)
	if err != nil {
		return nil, fmt.Errorf("read publish spec: %w", err)
	}
	var spec Spec
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return nil, fmt.Errorf("parse publish spec: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("parse publish spec: multiple JSON values")
		}
		return nil, fmt.Errorf("parse publish spec: %w", err)
	}
	specDir := filepath.Dir(absSpec)
	if err := applySpecDefaults(&spec, specDir, opts); err != nil {
		return nil, err
	}
	v, err := version.Parse(spec.Version)
	if err != nil {
		return nil, fmt.Errorf("version: %w", err)
	}
	spec.Version = v.String()
	if spec.PublishedAt != "" {
		if _, err := time.Parse(time.RFC3339, spec.PublishedAt); err != nil {
			return nil, fmt.Errorf("published_at: %w", err)
		}
	} else {
		spec.PublishedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if spec.NotesFile != "" {
		notesPath := spec.NotesFile
		if !filepath.IsAbs(notesPath) {
			notesPath = filepath.Join(specDir, notesPath)
		}
		notes, err := os.ReadFile(notesPath)
		if err != nil {
			return nil, fmt.Errorf("read notes_file: %w", err)
		}
		if len(notes) > 1<<20 {
			return nil, errors.New("notes_file exceeds 1 MiB")
		}
		spec.Notes = string(notes)
	}
	if len(spec.Assets) == 0 {
		return nil, errors.New("publish spec has no assets")
	}

	outDir, err := filepath.Abs(opts.OutDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	m := &manifest.Manifest{
		SchemaVersion: manifest.SchemaVersion,
		AppID:         spec.AppID,
		Channel:       strings.ToLower(spec.Channel),
		Version:       spec.Version,
		PublishedAt:   spec.PublishedAt,
		Notes:         spec.Notes,
	}
	res := &Result{}
	seen := make(map[string]bool)
	for i, a := range spec.Assets {
		urlPath, err := cleanAssetURL(a.URL)
		if err != nil {
			return nil, fmt.Errorf("asset %d: %w", i, err)
		}
		dest := filepath.Join(outDir, filepath.FromSlash(urlPath))
		if !within(outDir, dest) {
			return nil, fmt.Errorf("asset %d: output path escapes out directory", i)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil, err
		}
		if a.Dir != "" {
			if within(a.Dir, outDir) {
				return nil, fmt.Errorf("asset %d: out directory must not be inside the source directory", i)
			}
			format := normalizeFormat(a.Format)
			if format == "" {
				format = formatFromName(urlPath)
			}
			if format != "zip" && format != "tar.gz" && format != "tar" {
				format = "zip"
			}
			if err := archive.Create(a.Dir, dest, format); err != nil {
				return nil, fmt.Errorf("asset %d: create archive: %w", i, err)
			}
		} else {
			if err := copyFileAtomic(a.Path, dest); err != nil {
				return nil, fmt.Errorf("asset %d: copy artifact: %w", i, err)
			}
		}
		digest, size, err := checksum.File(dest)
		if err != nil {
			return nil, fmt.Errorf("asset %d: hash artifact: %w", i, err)
		}
		name := a.Name
		if name == "" {
			name = path.Base(urlPath)
		}
		if seen[name] {
			return nil, fmt.Errorf("asset %d: duplicate manifest name %q", i, name)
		}
		seen[name] = true
		assetURL := urlPath
		if spec.BaseURL != "" {
			assetURL, err = joinURL(spec.BaseURL, urlPath)
			if err != nil {
				return nil, fmt.Errorf("asset %d: %w", i, err)
			}
		}
		format := normalizeFormat(a.Format)
		if format == "" {
			format = formatFromName(urlPath)
		}
		m.Assets = append(m.Assets, manifest.Asset{
			Name:   name,
			OS:     a.OS,
			Arch:   a.Arch,
			URL:    assetURL,
			SHA256: digest,
			Size:   size,
			Format: format,
		})
		res.Assets = append(res.Assets, dest)
	}
	raw, err := manifest.Marshal(m)
	if err != nil {
		return nil, err
	}
	manifestName := opts.ManifestName
	if manifestName == "" {
		manifestName = "manifest.json"
	}
	if manifestName != filepath.Base(manifestName) || manifestName == "." || manifestName == ".." {
		return nil, errors.New("manifest name must be a plain filename")
	}
	manifestPath := filepath.Join(outDir, manifestName)
	if err := writeAtomic(manifestPath, raw, 0o644); err != nil {
		return nil, err
	}
	res.ManifestPath = manifestPath

	checksumsPath := filepath.Join(outDir, "checksums.txt")
	checksumsData := manifest.Checksums(m)
	if err := writeAtomic(checksumsPath, checksumsData, 0o644); err != nil {
		return nil, err
	}
	res.ChecksumsPath = checksumsPath

	if opts.SigningKey != "" {
		priv, err := sign.LoadPrivateKey(opts.SigningKey)
		if err != nil {
			return nil, err
		}
		sig := sign.Sign(priv, raw)
		manifestSigPath := manifestPath + ".sig"
		if err := writeAtomic(manifestSigPath, []byte(sig+"\n"), 0o644); err != nil {
			return nil, err
		}
		res.SignaturePath = manifestSigPath
		sumsSig := sign.Sign(priv, checksumsData)
		if err := writeAtomic(checksumsPath+".sig", []byte(sumsSig+"\n"), 0o644); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func applySpecDefaults(spec *Spec, specDir string, opts Options) error {
	if spec.AppID == "" {
		return errors.New("app_id is required")
	}
	if spec.Channel == "" {
		spec.Channel = "stable"
	}
	if opts.BaseURL != "" {
		spec.BaseURL = opts.BaseURL
	}
	if spec.BaseURL != "" {
		if u, err := url.Parse(spec.BaseURL); err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("invalid base_url %q", spec.BaseURL)
		}
	}
	if len(spec.Assets) > 256 {
		return errors.New("publish spec has more than 256 assets")
	}
	usedURLs := make(map[string]bool)
	for i := range spec.Assets {
		a := &spec.Assets[i]
		if (a.Path == "") == (a.Dir == "") {
			return fmt.Errorf("asset %d: set exactly one of path or dir", i)
		}
		if a.Path != "" && !filepath.IsAbs(a.Path) {
			a.Path = filepath.Join(specDir, a.Path)
		}
		if a.Dir != "" && !filepath.IsAbs(a.Dir) {
			a.Dir = filepath.Join(specDir, a.Dir)
		}
		if a.OS != "" {
			normalized, err := platform.NormalizeOS(a.OS)
			if err != nil {
				return fmt.Errorf("asset %d: %w", i, err)
			}
			a.OS = normalized
		}
		if a.Arch != "" {
			normalized, err := platform.NormalizeArch(a.Arch)
			if err != nil {
				return fmt.Errorf("asset %d: %w", i, err)
			}
			a.Arch = normalized
		}
		if a.URL == "" {
			base := a.Path
			if a.Dir != "" {
				base = a.Dir
			}
			a.URL = filepath.Base(base)
			if a.Dir != "" {
				ext := ".zip"
				if a.Format == "tar.gz" {
					ext = ".tar.gz"
				} else if a.Format == "tar" {
					ext = ".tar"
				}
				a.URL = strings.TrimSuffix(a.URL, filepath.Ext(a.URL)) + ext
			}
		}
		clean, err := cleanAssetURL(a.URL)
		if err != nil {
			return fmt.Errorf("asset %d: %w", i, err)
		}
		a.URL = clean
		if usedURLs[clean] {
			return fmt.Errorf("asset %d: duplicate output URL %q", i, clean)
		}
		usedURLs[clean] = true
	}
	return nil
}

func cleanAssetURL(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, `\`, "/"))
	if raw == "" {
		return "", errors.New("asset URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "" || u.Host != "" || strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("asset URL %q must be relative", raw)
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("asset URL %q escapes the output directory", raw)
	}
	return clean, nil
}

func normalizeFormat(s string) string {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "."))
	switch s {
	case "tgz", "tar.gz":
		return "tar.gz"
	default:
		return s
	}
}

func formatFromName(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return "tar.gz"
	case strings.HasSuffix(lower, ".zip"):
		return "zip"
	case strings.HasSuffix(lower, ".tar"):
		return "tar"
	case strings.HasSuffix(lower, ".gz"):
		return "gz"
	case strings.HasSuffix(lower, ".exe"):
		return "exe"
	case strings.HasSuffix(lower, ".msi"):
		return "msi"
	case strings.HasSuffix(lower, ".pkg"):
		return "pkg"
	case strings.HasSuffix(lower, ".dmg"):
		return "dmg"
	default:
		return "raw"
	}
}

func joinURL(base, rel string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(rel, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".publish-*.tmp")
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
	if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".publish-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
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
