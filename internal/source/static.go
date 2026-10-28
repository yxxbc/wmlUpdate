package source

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yxxbc/wmlUpdate/internal/httpx"
	"github.com/yxxbc/wmlUpdate/internal/manifest"
	"github.com/yxxbc/wmlUpdate/internal/platform"
	"github.com/yxxbc/wmlUpdate/internal/release"
	"github.com/yxxbc/wmlUpdate/internal/version"
)

type staticProvider struct {
	base
}

func (p *staticProvider) Name() string { return "static" }

func (p *staticProvider) Fetch(ctx context.Context) (*release.Release, error) {
	cfg := p.cfg
	manifestURLs := cfg.Source.ManifestURLs
	if len(manifestURLs) == 0 && cfg.Source.ManifestURL != "" {
		manifestURLs = []string{cfg.Source.ManifestURL}
	}
	if len(manifestURLs) == 0 {
		return nil, fmt.Errorf("no manifest URL configured")
	}
	var failures []string
	for _, manifestURL := range manifestURLs {
		rel, err := p.fetchOne(ctx, manifestURL, manifestURLs)
		if err == nil {
			return rel, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		failures = append(failures, fmt.Sprintf("%s: %v", hostOf(manifestURL), err))
	}
	return nil, fmt.Errorf("all %d static manifest mirrors failed: %s", len(manifestURLs), strings.Join(failures, "; "))
}

func (p *staticProvider) fetchOne(ctx context.Context, manifestURL string, allManifestURLs []string) (*release.Release, error) {
	cfg := p.cfg
	headers := cfg.Source.Headers
	body, err := p.fetchMetadata(ctx, manifestURL, headers, 4<<20)
	if err != nil {
		return nil, fmt.Errorf("download manifest: %w", err)
	}

	pub, err := cfg.PublicKey()
	if err != nil {
		return nil, err
	}
	if len(pub) > 0 {
		sigURL, err := manifest.SignatureURL(manifestURL)
		if err != nil {
			return nil, fmt.Errorf("build manifest signature URL: %w", err)
		}
		sigBody, _, status, err := httpx.GetBytes(ctx, p.client, sigURL, headers, 1<<20, p.retries)
		if err != nil {
			return nil, fmt.Errorf("download manifest signature: %w", err)
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("download manifest signature: unexpected HTTP %d", status)
		}
		if err := manifest.Verify(body, string(sigBody), ed25519.PublicKey(pub)); err != nil {
			return nil, err
		}
	}

	m, err := manifest.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.AppID != cfg.AppID {
		return nil, fmt.Errorf("manifest app_id %q does not match config app_id %q", m.AppID, cfg.AppID)
	}
	if !strings.EqualFold(m.Channel, cfg.Channel) {
		return nil, fmt.Errorf("manifest channel %q does not match configured channel %q", m.Channel, cfg.Channel)
	}
	v, err := version.Parse(m.Version)
	if err != nil {
		return nil, fmt.Errorf("manifest version: %w", err)
	}
	baseURL, err := url.Parse(manifestURL)
	if err != nil {
		return nil, fmt.Errorf("parse manifest URL: %w", err)
	}
	rel := &release.Release{
		Version:    v,
		Tag:        m.Version,
		Notes:      m.Notes,
		Prerelease: v.IsPrerelease(),
		Mandatory:  m.Mandatory,
		Channel:    m.Channel,
		Source:     "static",
	}
	if m.PublishedAt != "" {
		rel.PublishedAt, _ = time.Parse(time.RFC3339, m.PublishedAt)
	}
	for i, a := range m.Assets {
		ref, err := url.Parse(a.URL)
		if err != nil {
			return nil, fmt.Errorf("asset %d URL: %w", i, err)
		}
		target := baseURL.ResolveReference(ref)
		if target.Scheme != "http" && target.Scheme != "https" {
			return nil, fmt.Errorf("asset %d has unsupported URL scheme %q", i, target.Scheme)
		}
		assetHeaders := headerForHost(headers, baseURL.Host, target.Host)
		asset := release.Asset{
			Name:    a.Name,
			URL:     target.String(),
			Format:  a.Format,
			OS:      a.OS,
			Arch:    a.Arch,
			SHA256:  a.SHA256,
			Size:    a.Size,
			Headers: assetHeaders,
		}
		if asset.OS != "" {
			if normalized, err := platform.NormalizeOS(asset.OS); err == nil {
				asset.OS = normalized
			}
		}
		if asset.Arch != "" {
			if normalized, err := platform.NormalizeArch(asset.Arch); err == nil {
				asset.Arch = normalized
			}
		}
		// A mirrored manifest usually serves the same relative asset layout.
		// Resolve relative assets against every configured mirror so a failed
		// download can move to another host without re-fetching metadata.
		if !ref.IsAbs() {
			seen := map[string]bool{asset.URL: true}
			for _, otherManifest := range allManifestURLs {
				otherBase, err := url.Parse(otherManifest)
				if err != nil {
					continue
				}
				u := otherBase.ResolveReference(ref).String()
				if !seen[u] {
					seen[u] = true
					asset.FallbackURLs = append(asset.FallbackURLs, u)
				}
			}
		}
		rel.Assets = append(rel.Assets, asset)
	}
	applyAssetMirrors(rel, cfg.Source.AssetMirrors)
	return rel, nil
}
