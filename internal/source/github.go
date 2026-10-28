package source

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/yxxbc/wmlUpdate/internal/checksum"
	"github.com/yxxbc/wmlUpdate/internal/httpx"
	"github.com/yxxbc/wmlUpdate/internal/release"
	"github.com/yxxbc/wmlUpdate/internal/sign"
	"github.com/yxxbc/wmlUpdate/internal/version"
)

type githubProvider struct {
	base
}

func (p *githubProvider) Name() string { return "github" }

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

type ghAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
	Size               int64  `json:"size"`
}

func (p *githubProvider) Fetch(ctx context.Context) (*release.Release, error) {
	cfg := p.cfg
	token, err := p.githubToken()
	if err != nil {
		return nil, err
	}
	apiURL := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=100",
		cfg.Source.APIBase, url.PathEscape(cfg.Source.Owner), url.PathEscape(cfg.Source.Repo))
	headers := map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	body, _, status, err := httpx.GetBytes(ctx, p.client, apiURL, headers, 32<<20, p.retries)
	if err != nil {
		var se *httpx.StatusError
		if errors.As(err, &se) && se.StatusCode == http.StatusForbidden && strings.Contains(strings.ToLower(se.Body), "rate limit") {
			return nil, fmt.Errorf("GitHub API rate limit exceeded; set source.token_env to a token with public_repo access: %w", err)
		}
		return nil, fmt.Errorf("list GitHub releases: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("list GitHub releases: unexpected HTTP %d", status)
	}
	var releases []ghRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, fmt.Errorf("decode GitHub releases: %w", err)
	}

	includePre := cfg.Source.IncludePrerelease || cfg.Channel != "stable"
	var selected *ghRelease
	var selectedVersion version.Version
	for i := range releases {
		rel := &releases[i]
		if rel.Draft {
			continue
		}
		tag := rel.TagName
		if cfg.Source.TagPrefix != "" {
			if !strings.HasPrefix(tag, cfg.Source.TagPrefix) {
				continue
			}
			tag = strings.TrimPrefix(tag, cfg.Source.TagPrefix)
		}
		v, err := version.Parse(tag)
		if err != nil {
			continue
		}
		if (rel.Prerelease || v.IsPrerelease()) && !includePre {
			continue
		}
		if selected == nil || v.Compare(selectedVersion) > 0 {
			selected = rel
			selectedVersion = v
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("%w: GitHub repository %s/%s has no releases for channel %q",
			ErrNoRelease, cfg.Source.Owner, cfg.Source.Repo, cfg.Channel)
	}

	rel := &release.Release{
		Version:     selectedVersion,
		Tag:         selected.TagName,
		Notes:       selected.Body,
		PublishedAt: selected.PublishedAt,
		Prerelease:  selected.Prerelease || selectedVersion.IsPrerelease(),
		Channel:     cfg.Channel,
		Source:      "github",
		Owner:       cfg.Source.Owner,
		Repo:        cfg.Source.Repo,
	}
	for _, a := range selected.Assets {
		targetURL, assetHeaders := p.assetTarget(a, token)
		sha := parseDigest(a.Digest)
		rel.Assets = append(rel.Assets, release.Asset{
			Name:    a.Name,
			URL:     targetURL,
			Format:  inferFormat(a.Name),
			SHA256:  sha,
			Size:    a.Size,
			Headers: assetHeaders,
		})
	}

	if err := p.applyChecksums(ctx, rel, selected.Assets, token); err != nil {
		return nil, err
	}
	applyAssetMirrors(rel, cfg.Source.AssetMirrors)
	return rel, nil
}

func (p *githubProvider) githubToken() (string, error) {
	if p.cfg.Source.TokenEnv == "" {
		return "", nil
	}
	token, ok := os.LookupEnv(p.cfg.Source.TokenEnv)
	if !ok || token == "" {
		return "", fmt.Errorf("source.token_env %q is not set; refusing to continue without the configured GitHub token", p.cfg.Source.TokenEnv)
	}
	return token, nil
}

func (p *githubProvider) assetTarget(a ghAsset, token string) (string, map[string]string) {
	if token != "" && a.ID != 0 {
		u := fmt.Sprintf("%s/repos/%s/%s/releases/assets/%d",
			p.cfg.Source.APIBase, url.PathEscape(p.cfg.Source.Owner), url.PathEscape(p.cfg.Source.Repo), a.ID)
		return u, map[string]string{
			"Accept":        "application/octet-stream",
			"Authorization": "Bearer " + token,
		}
	}
	return a.BrowserDownloadURL, nil
}

func parseDigest(d string) string {
	d = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), "sha256:")
	if len(d) != 64 {
		return ""
	}
	return d
}

func (p *githubProvider) applyChecksums(ctx context.Context, rel *release.Release, assets []ghAsset, token string) error {
	cfg := p.cfg
	pub, err := cfg.PublicKey()
	if err != nil {
		return err
	}
	checksumsAsset := findAsset(assets, cfg.Security.ChecksumsAsset)
	signatureAsset := findAsset(assets, cfg.Security.ChecksumsSignatureAsset)

	if checksumsAsset == nil {
		if len(pub) > 0 {
			return fmt.Errorf("security.public_key is configured but GitHub release %s has no %s asset", rel.Tag, cfg.Security.ChecksumsAsset)
		}
		return nil
	}

	sumsURL, sumsHeaders := p.assetTarget(*checksumsAsset, token)
	sumsBody, err := p.downloadSmall(ctx, sumsURL, sumsHeaders)
	if err != nil {
		return fmt.Errorf("download %s: %w", cfg.Security.ChecksumsAsset, err)
	}
	if len(pub) > 0 {
		if signatureAsset == nil {
			return fmt.Errorf("security.public_key is configured but release has no %s asset", cfg.Security.ChecksumsSignatureAsset)
		}
		sigURL, sigHeaders := p.assetTarget(*signatureAsset, token)
		sigBody, err := p.downloadSmall(ctx, sigURL, sigHeaders)
		if err != nil {
			return fmt.Errorf("download %s: %w", cfg.Security.ChecksumsSignatureAsset, err)
		}
		if err := sign.Verify(ed25519.PublicKey(pub), sumsBody, string(sigBody)); err != nil {
			return fmt.Errorf("verify %s: %w", cfg.Security.ChecksumsAsset, err)
		}
	}
	sums, err := checksum.Parse(sumsBody)
	if err != nil {
		return fmt.Errorf("parse %s: %w", cfg.Security.ChecksumsAsset, err)
	}
	for i := range rel.Assets {
		if digest, ok := checksum.Lookup(sums, rel.Assets[i].Name); ok {
			rel.Assets[i].SHA256 = digest
		}
	}
	return nil
}

func (p *githubProvider) downloadSmall(ctx context.Context, rawURL string, headers map[string]string) ([]byte, error) {
	if rawURL == "" {
		return nil, errors.New("asset has no download URL")
	}
	body, _, _, err := httpx.GetBytes(ctx, p.client, rawURL, headers, 16<<20, p.retries)
	return body, err
}

func findAsset(assets []ghAsset, name string) *ghAsset {
	if name == "" {
		return nil
	}
	for i := range assets {
		if strings.EqualFold(assets[i].Name, name) || strings.EqualFold(path.Base(assets[i].Name), path.Base(name)) {
			return &assets[i]
		}
	}
	return nil
}

func inferFormat(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"):
		return "tar.gz"
	case strings.HasSuffix(lower, ".tgz"):
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
