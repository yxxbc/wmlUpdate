package source

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/yxxbc/wmlUpdate/internal/release"
	"github.com/yxxbc/wmlUpdate/internal/version"
)

// FallbackProvider is implemented by a source chain. It is consulted only
// after the primary asset download fails, and returns releases that may contain
// the same version from another mirror.
type FallbackProvider interface {
	FallbackReleases(ctx context.Context, target version.Version) ([]*release.Release, error)
}

type chainProvider struct {
	base
	sources []Provider
}

func (p *chainProvider) Name() string { return "chain" }

func (p *chainProvider) Fetch(ctx context.Context) (*release.Release, error) {
	if len(p.sources) == 0 {
		return nil, errors.New("source chain has no sources")
	}
	var failures []string
	for _, source := range p.sources {
		rel, err := source.Fetch(ctx)
		if err == nil {
			applyAssetMirrors(rel, p.cfg.Source.AssetMirrors)
			return rel, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		failures = append(failures, fmt.Sprintf("%s: %v", source.Name(), err))
	}
	return nil, fmt.Errorf("all %d update sources failed: %s", len(p.sources), strings.Join(failures, "; "))
}

// FallbackReleases fetches the configured sources after a download failure and
// returns releases for the same target version. The engine still requires a
// matching SHA-256 before accepting any fallback asset.
func (p *chainProvider) FallbackReleases(ctx context.Context, target version.Version) ([]*release.Release, error) {
	var out []*release.Release
	var failures []string
	for _, source := range p.sources {
		rel, err := source.Fetch(ctx)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", source.Name(), err))
			continue
		}
		if rel.Version.Compare(target) != 0 {
			continue
		}
		applyAssetMirrors(rel, p.cfg.Source.AssetMirrors)
		out = append(out, rel)
	}
	if len(out) == 0 {
		if len(failures) > 0 {
			return nil, fmt.Errorf("no fallback release for %s: %s", target, strings.Join(failures, "; "))
		}
		return nil, fmt.Errorf("no fallback source offers version %s", target)
	}
	return out, nil
}

func applyAssetMirrors(rel *release.Release, templates []string) {
	if rel == nil || len(templates) == 0 {
		return
	}
	for i := range rel.Assets {
		asset := &rel.Assets[i]
		seen := map[string]bool{asset.URL: true}
		for _, u := range asset.FallbackURLs {
			seen[u] = true
		}
		for _, template := range templates {
			u := expandMirrorTemplate(template, rel, *asset)
			if u == "" || seen[u] {
				continue
			}
			seen[u] = true
			asset.FallbackURLs = append(asset.FallbackURLs, u)
		}
	}
}

func expandMirrorTemplate(template string, rel *release.Release, asset release.Asset) string {
	assetName := asset.Name
	if assetName == "" {
		if u, err := url.Parse(asset.URL); err == nil {
			assetName = u.Path[strings.LastIndex(u.Path, "/")+1:]
		}
	}
	replacer := strings.NewReplacer(
		"{url}", asset.URL,
		"{owner}", url.PathEscape(rel.Owner),
		"{repo}", url.PathEscape(rel.Repo),
		"{tag}", url.PathEscape(rel.Tag),
		"{version}", url.PathEscape(rel.Version.String()),
		"{asset}", url.PathEscape(assetName),
		"{name}", url.PathEscape(assetName),
		"{os}", url.PathEscape(asset.OS),
		"{arch}", url.PathEscape(asset.Arch),
	)
	out := strings.TrimSpace(replacer.Replace(template))
	return out
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "static mirror"
	}
	return u.Host
}
