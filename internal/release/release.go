// Package release contains the normalized release model shared by update
// sources and the update engine.
package release

import (
	"time"

	"github.com/yxxbc/wmlUpdate/internal/version"
)

// Release is a normalized update release.
type Release struct {
	Version     version.Version
	Tag         string
	Notes       string
	PublishedAt time.Time
	Prerelease  bool
	Mandatory   bool
	Channel     string
	Source      string
	Owner       string
	Repo        string
	Assets      []Asset
}

// Asset is a downloadable release artifact. SHA256 is always populated.
type Asset struct {
	Name         string            `json:"name"`
	URL          string            `json:"url"`
	Format       string            `json:"format,omitempty"`
	OS           string            `json:"os,omitempty"`
	Arch         string            `json:"arch,omitempty"`
	SHA256       string            `json:"sha256"`
	Size         int64             `json:"size,omitempty"`
	Headers      map[string]string `json:"-"`
	FallbackURLs []string          `json:"-"`
}
