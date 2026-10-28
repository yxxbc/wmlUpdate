// Package manifest defines the self-hosted update manifest format and its
// detached Ed25519 signature.
package manifest

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/yxxbc/wmlUpdate/internal/sign"
	"github.com/yxxbc/wmlUpdate/internal/version"
)

const SchemaVersion = 1

// Manifest describes one published release. It is served as manifest.json and
// signed by writing the Ed25519 signature of the exact file bytes to
// manifest.json.sig.
type Manifest struct {
	SchemaVersion int     `json:"schema_version"`
	AppID         string  `json:"app_id"`
	Channel       string  `json:"channel"`
	Version       string  `json:"version"`
	PublishedAt   string  `json:"published_at,omitempty"`
	Notes         string  `json:"notes,omitempty"`
	Mandatory     bool    `json:"mandatory,omitempty"`
	Assets        []Asset `json:"assets"`
}

// Asset is one artifact entry. URL may be absolute or relative to the
// manifest URL.
type Asset struct {
	Name   string `json:"name,omitempty"`
	OS     string `json:"os,omitempty"`
	Arch   string `json:"arch,omitempty"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
	Format string `json:"format,omitempty"`
}

// Parse decodes and validates manifest bytes.
func Parse(data []byte) (*Manifest, error) {
	if len(data) > 4<<20 {
		return nil, errors.New("manifest exceeds 4 MiB")
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("parse manifest: multiple JSON values")
		}
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks invariants required by the updater.
func (m *Manifest) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("manifest schema_version %d is unsupported (want %d)", m.SchemaVersion, SchemaVersion)
	}
	if strings.TrimSpace(m.AppID) == "" {
		return errors.New("manifest app_id is empty")
	}
	if strings.TrimSpace(m.Channel) == "" {
		return errors.New("manifest channel is empty")
	}
	if _, err := version.Parse(m.Version); err != nil {
		return fmt.Errorf("manifest version: %w", err)
	}
	if m.PublishedAt != "" {
		if _, err := time.Parse(time.RFC3339, m.PublishedAt); err != nil {
			return fmt.Errorf("manifest published_at: %w", err)
		}
	}
	if len(m.Notes) > 1<<20 {
		return errors.New("manifest notes exceed 1 MiB")
	}
	if len(m.Assets) == 0 {
		return errors.New("manifest has no assets")
	}
	if len(m.Assets) > 256 {
		return errors.New("manifest has more than 256 assets")
	}
	seen := make(map[string]bool, len(m.Assets))
	for i := range m.Assets {
		a := &m.Assets[i]
		if strings.TrimSpace(a.URL) == "" {
			return fmt.Errorf("asset %d: url is empty", i)
		}
		if _, err := url.Parse(a.URL); err != nil {
			return fmt.Errorf("asset %d: invalid url: %w", i, err)
		}
		a.SHA256 = strings.ToLower(strings.TrimSpace(a.SHA256))
		if len(a.SHA256) != 64 {
			return fmt.Errorf("asset %d: sha256 must be 64 hex characters", i)
		}
		if _, err := hex.DecodeString(a.SHA256); err != nil {
			return fmt.Errorf("asset %d: invalid sha256", i)
		}
		if a.Size < 0 {
			return fmt.Errorf("asset %d: size cannot be negative", i)
		}
		if a.Name == "" {
			if u, err := url.Parse(a.URL); err == nil {
				a.Name = path.Base(u.Path)
			}
			if a.Name == "" || a.Name == "." || a.Name == "/" {
				return fmt.Errorf("asset %d: name is required for URLs without a filename", i)
			}
		}
		key := a.Name
		if seen[key] {
			return fmt.Errorf("asset %d: duplicate name %q", i, key)
		}
		seen[key] = true
		a.Format = normalizeFormat(a.Format)
	}
	return nil
}

func normalizeFormat(s string) string {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "."))
	switch s {
	case "tgz", "tar.gz":
		return "tar.gz"
	case "gz":
		return "gz"
	default:
		return s
	}
}

// Marshal encodes a manifest deterministically enough for signing and writes a
// trailing newline.
func Marshal(m *Manifest) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Sign returns the base64 detached signature of raw manifest bytes.
func Sign(raw []byte, priv ed25519.PrivateKey) string {
	return sign.Sign(priv, raw)
}

// Verify checks a detached manifest signature.
func Verify(raw []byte, signature string, pub ed25519.PublicKey) error {
	if err := sign.Verify(pub, raw, signature); err != nil {
		return fmt.Errorf("manifest signature: %w", err)
	}
	return nil
}

// Checksums renders a GNU sha256sum-compatible file for a release.
func Checksums(m *Manifest) []byte {
	type entry struct{ digest, name string }
	entries := make([]entry, 0, len(m.Assets))
	for _, a := range m.Assets {
		name := a.Name
		if name == "" {
			name = path.Base(a.URL)
		}
		entries = append(entries, entry{digest: a.SHA256, name: name})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.digest)
		b.WriteString("  ")
		b.WriteString(e.name)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// SignatureURL returns the sidecar URL for a manifest URL.
func SignatureURL(manifestURL string) (string, error) {
	u, err := url.Parse(manifestURL)
	if err != nil {
		return "", err
	}
	u.Path += ".sig"
	return u.String(), nil
}
