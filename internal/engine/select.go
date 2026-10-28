package engine

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/yxxbc/wmlUpdate/internal/config"
	"github.com/yxxbc/wmlUpdate/internal/platform"
	"github.com/yxxbc/wmlUpdate/internal/release"
)

func selectAsset(rel *release.Release, cfg *config.Config, p platform.Platform) (release.Asset, error) {
	candidates := make([]release.Asset, 0, len(rel.Assets))
	for _, a := range rel.Assets {
		if isMetadataAsset(a.Name, cfg) {
			continue
		}
		if a.Name == "" {
			a.Name = assetNameFromURL(a.URL)
		}
		candidates = append(candidates, a)
	}
	if len(candidates) == 0 {
		return release.Asset{}, fmt.Errorf("release %s has no downloadable assets", rel.Tag)
	}

	for _, rule := range cfg.AssetRules {
		if rule.OS != "" && rule.OS != p.OS {
			continue
		}
		if rule.Arch != "" && rule.Arch != p.Arch {
			continue
		}
		var matches []release.Asset
		if rule.Match == "" {
			for _, a := range candidates {
				if a.OS == "" || a.Arch == "" {
					continue
				}
				if strings.EqualFold(a.OS, p.OS) && strings.EqualFold(a.Arch, p.Arch) {
					matches = append(matches, a)
				}
			}
		} else {
			pattern := expandAssetPattern(rule.Match, rel, p)
			for _, a := range candidates {
				if ok, err := path.Match(pattern, a.Name); err == nil && ok {
					matches = append(matches, a)
				}
			}
		}
		switch len(matches) {
		case 0:
			continue
		case 1:
			chosen := matches[0]
			if rule.Format != "" {
				chosen.Format = rule.Format
			}
			fillAsset(&chosen, p)
			return chosen, nil
		default:
			return release.Asset{}, fmt.Errorf("asset rule matched multiple files for %s: %s", p, assetNames(matches))
		}
	}

	// Metadata supplied by a self-hosted manifest is authoritative.
	var exact []release.Asset
	var osOnly []release.Asset
	for _, a := range candidates {
		if a.OS == "" && a.Arch == "" {
			continue
		}
		if strings.EqualFold(a.OS, p.OS) {
			if strings.EqualFold(a.Arch, p.Arch) {
				exact = append(exact, a)
			} else if a.Arch == "" {
				osOnly = append(osOnly, a)
			}
		}
	}
	if len(exact) == 1 {
		fillAsset(&exact[0], p)
		return exact[0], nil
	}
	if len(exact) > 1 {
		return release.Asset{}, fmt.Errorf("multiple assets are tagged for %s: %s; add asset_rules", p, assetNames(exact))
	}
	if len(osOnly) == 1 {
		fillAsset(&osOnly[0], p)
		return osOnly[0], nil
	}
	if len(osOnly) > 1 {
		return release.Asset{}, fmt.Errorf("multiple architecture-neutral assets for %s: %s; add asset_rules", p.OS, assetNames(osOnly))
	}

	var heuristic []release.Asset
	for _, a := range candidates {
		if platform.AssetNameMatches(a.Name, p) {
			heuristic = append(heuristic, a)
		}
	}
	if len(heuristic) == 1 {
		fillAsset(&heuristic[0], p)
		return heuristic[0], nil
	}
	if len(heuristic) > 1 {
		return release.Asset{}, fmt.Errorf("asset name heuristic is ambiguous for %s: %s; add asset_rules", p, assetNames(heuristic))
	}
	return release.Asset{}, fmt.Errorf("no asset found for %s in release %s; available: %s; add asset_rules",
		p, rel.Tag, assetNames(candidates))
}

func fillAsset(a *release.Asset, p platform.Platform) {
	if a.OS == "" {
		a.OS = p.OS
	}
	if a.Arch == "" {
		a.Arch = p.Arch
	}
	if a.Format == "" {
		a.Format = inferFormat(a.Name)
	}
}

func assetNameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return filepath.Base(rawURL)
	}
	return path.Base(u.Path)
}

func expandAssetPattern(pattern string, rel *release.Release, p platform.Platform) string {
	r := strings.NewReplacer(
		"{version}", rel.Version.String(),
		"{tag}", rel.Tag,
		"{os}", p.OS,
		"{arch}", p.Arch,
	)
	return r.Replace(pattern)
}

func isMetadataAsset(name string, cfg *config.Config) bool {
	if name == "" {
		return false
	}
	base := strings.ToLower(path.Base(name))
	if base == strings.ToLower(cfg.Security.ChecksumsAsset) || base == strings.ToLower(cfg.Security.ChecksumsSignatureAsset) {
		return true
	}
	for _, suffix := range []string{".sig", ".sha256", ".sha256sum", ".asc", ".minisig"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

func assetNames(assets []release.Asset) string {
	names := make([]string, len(assets))
	for i, a := range assets {
		names[i] = a.Name
	}
	return strings.Join(names, ", ")
}

func inferFormat(name string) string {
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
