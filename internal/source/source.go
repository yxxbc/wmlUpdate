// Package source fetches normalized release metadata from GitHub Releases or a
// self-hosted static manifest.
package source

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/yxxbc/wmlUpdate/internal/config"
	"github.com/yxxbc/wmlUpdate/internal/httpx"
	"github.com/yxxbc/wmlUpdate/internal/release"
)

// ErrNoRelease means the source had no release eligible for the configured
// channel.
var ErrNoRelease = errors.New("no matching release found")

// Provider discovers the latest eligible release.
type Provider interface {
	Fetch(ctx context.Context) (*release.Release, error)
	Name() string
}

// New constructs the configured provider.
func New(cfg *config.Config, client *http.Client, cacheDir string) (Provider, error) {
	cache := newMetadataCache(cacheDir)
	return newProvider(cfg, cfg.Source, client, cache)
}

func newProvider(cfg *config.Config, sc config.SourceConfig, client *http.Client, cache *metadataCache) (Provider, error) {
	child := *cfg
	child.Source = sc
	common := base{cfg: &child, client: client, cache: cache, retries: cfg.Download.Retries}
	switch sc.Type {
	case "github":
		return &githubProvider{base: common}, nil
	case "static":
		return &staticProvider{base: common}, nil
	case "chain":
		chain := &chainProvider{base: common}
		for i, sourceCfg := range sc.Sources {
			provider, err := newProvider(cfg, sourceCfg, client, cache)
			if err != nil {
				return nil, fmt.Errorf("source.sources[%d]: %w", i, err)
			}
			chain.sources = append(chain.sources, provider)
		}
		return chain, nil
	default:
		return nil, fmt.Errorf("unsupported source type %q", sc.Type)
	}
}

type base struct {
	cfg     *config.Config
	client  *http.Client
	cache   *metadataCache
	retries int
}

func (b *base) fetchMetadata(ctx context.Context, rawURL string, headers map[string]string, maxBytes int64) ([]byte, error) {
	reqHeaders := make(map[string]string, len(headers)+2)
	for k, v := range headers {
		reqHeaders[k] = v
	}
	cached, hasCache := b.cache.get(rawURL)
	if hasCache {
		if cached.ETag != "" {
			reqHeaders["If-None-Match"] = cached.ETag
		}
		if cached.LastModified != "" {
			reqHeaders["If-Modified-Since"] = cached.LastModified
		}
	}
	body, respHeaders, status, err := httpx.GetBytes(ctx, b.client, rawURL, reqHeaders, maxBytes, b.retries)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotModified {
		if !hasCache {
			return nil, errors.New("server returned 304 but no cached metadata exists")
		}
		return cached.Body, nil
	}
	if len(body) == 0 {
		return nil, errors.New("empty metadata response")
	}
	entry := cachedResponse{
		URL:          rawURL,
		ETag:         respHeaders.Get("ETag"),
		LastModified: respHeaders.Get("Last-Modified"),
		Body:         body,
	}
	if err := b.cache.put(entry); err != nil {
		// Caching is an optimization; a read-only state directory must not make
		// checking for updates fail.
		_ = err
	}
	return body, nil
}

func headerForHost(headers map[string]string, sourceHost, targetHost string) map[string]string {
	if len(headers) == 0 || !strings.EqualFold(sourceHost, targetHost) {
		return nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[k] = v
	}
	return out
}
