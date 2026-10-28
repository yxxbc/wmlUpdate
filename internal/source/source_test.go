package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yxxbc/wmlUpdate/internal/config"
	"github.com/yxxbc/wmlUpdate/internal/httpx"
	"github.com/yxxbc/wmlUpdate/internal/manifest"
	"github.com/yxxbc/wmlUpdate/internal/sign"
)

func writeCfg(t *testing.T, dir, sourceJSON, securityJSON string) *config.Config {
	t.Helper()
	path := filepath.Join(dir, "updater.json")
	body := `{
	  "app_id": "com.example.source",
	  "channel": "stable",
	  "current_version": "1.0.0",
	  "source": ` + sourceJSON + `,
	  "install": {"type": "external"},
	  "state_dir": "state"` + securityJSON + `
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestStaticSignedManifest(t *testing.T) {
	priv, pub, _ := sign.Generate()
	pubPEM, _ := sign.EncodePublicPEM(pub)
	dir := t.TempDir()
	pubPath := filepath.Join(dir, "key.pub")
	if err := os.WriteFile(pubPath, pubPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	m := &manifest.Manifest{
		SchemaVersion: 1, AppID: "com.example.source", Channel: "stable", Version: "2.0.0",
		PublishedAt: "2026-01-01T00:00:00Z", Notes: "release",
		Assets: []manifest.Asset{{Name: "app.zip", OS: "linux", Arch: "amd64", URL: "app.zip", SHA256: strings.Repeat("a", 64), Size: 10, Format: "zip"}},
	}
	raw, err := manifest.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	sig := sign.Sign(priv, raw)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write(raw)
		case "/manifest.json.sig":
			_, _ = w.Write([]byte(sig + "\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := writeCfg(t, dir,
		`{"type":"static","manifest_url":"`+server.URL+`/manifest.json"}`,
		`,"security":{"public_key_file":"`+pubPath+`"}`)
	client := httpx.NewClient(5*time.Second, httpx.Policy{AllowInsecure: true})
	p, err := New(cfg, client, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rel, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version.String() != "2.0.0" || len(rel.Assets) != 1 || rel.Assets[0].SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("release = %+v", rel)
	}
}

func TestStaticRejectsTamperedManifest(t *testing.T) {
	priv, pub, _ := sign.Generate()
	pubPEM, _ := sign.EncodePublicPEM(pub)
	dir := t.TempDir()
	pubPath := filepath.Join(dir, "key.pub")
	_ = os.WriteFile(pubPath, pubPEM, 0o644)
	m := &manifest.Manifest{SchemaVersion: 1, AppID: "com.example.source", Channel: "stable", Version: "2.0.0",
		Assets: []manifest.Asset{{Name: "a.zip", URL: "a.zip", SHA256: strings.Repeat("a", 64)}}}
	raw, _ := manifest.Marshal(m)
	sig := sign.Sign(priv, raw)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest.json" {
			_, _ = w.Write(append(raw[:len(raw)-2], []byte("x\n")...))
			return
		}
		_, _ = w.Write([]byte(sig + "\n"))
	}))
	defer server.Close()
	cfg := writeCfg(t, dir, `{"type":"static","manifest_url":"`+server.URL+`/manifest.json"}`,
		`,"security":{"public_key_file":"`+pubPath+`"}`)
	client := httpx.NewClient(5*time.Second, httpx.Policy{AllowInsecure: true})
	p, _ := New(cfg, client, t.TempDir())
	if _, err := p.Fetch(context.Background()); err == nil {
		t.Fatal("tampered manifest was accepted")
	}
}

func TestGitHubReleaseSelectionAndChecksums(t *testing.T) {
	priv, pub, _ := sign.Generate()
	pubPEM, _ := sign.EncodePublicPEM(pub)
	dir := t.TempDir()
	pubPath := filepath.Join(dir, "key.pub")
	_ = os.WriteFile(pubPath, pubPEM, 0o644)
	digest := strings.Repeat("b", 64)
	sums := []byte(digest + "  app-linux-amd64.tar.gz\n")
	sumsSig := sign.Sign(priv, sums)

	relJSON := []map[string]any{
		{
			"tag_name": "v1.3.0-beta.1", "draft": false, "prerelease": true,
			"published_at": "2026-02-01T00:00:00Z",
			"assets": []map[string]any{
				{"id": 10, "name": "app-linux-amd64.tar.gz", "browser_download_url": "", "digest": "", "size": 100},
				{"id": 11, "name": "checksums.txt", "browser_download_url": "", "size": int64(len(sums))},
				{"id": 12, "name": "checksums.txt.sig", "browser_download_url": "", "size": int64(len(sumsSig))},
			},
		},
		{
			"tag_name": "v1.2.0", "draft": false, "prerelease": false,
			"published_at": "2026-01-01T00:00:00Z",
			"assets": []map[string]any{
				{"id": 1, "name": "app-linux-amd64.tar.gz", "browser_download_url": "", "digest": "", "size": 100},
				{"id": 2, "name": "checksums.txt", "browser_download_url": "", "size": int64(len(sums))},
				{"id": 3, "name": "checksums.txt.sig", "browser_download_url": "", "size": int64(len(sumsSig))},
			},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/app/releases":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(relJSON)
		case r.URL.Path == "/checksums.txt":
			_, _ = w.Write(sums)
		case r.URL.Path == "/checksums.txt.sig":
			_, _ = w.Write([]byte(sumsSig))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	// Fill in browser URLs now that the test server address is known.
	for _, rel := range relJSON {
		for _, a := range rel["assets"].([]map[string]any) {
			name := a["name"].(string)
			if name == "app-linux-amd64.tar.gz" {
				a["browser_download_url"] = server.URL + "/" + name
			} else {
				a["browser_download_url"] = server.URL + "/" + name
			}
		}
	}
	cfg := writeCfg(t, dir,
		`{"type":"github","owner":"acme","repo":"app","api_base":"`+server.URL+`"}`,
		`,"security":{"public_key_file":"`+pubPath+`"}`)
	client := httpx.NewClient(5*time.Second, httpx.Policy{AllowInsecure: true})
	p, err := New(cfg, client, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rel, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version.String() != "1.2.0" {
		t.Fatalf("stable version = %s", rel.Version)
	}
	if rel.Assets[0].SHA256 != digest {
		t.Fatalf("checksum = %q, want %q", rel.Assets[0].SHA256, digest)
	}

	cfg.Source.IncludePrerelease = true
	p2, _ := New(cfg, client, t.TempDir())
	rel, err = p2.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version.String() != "1.3.0-beta.1" {
		t.Fatalf("prerelease version = %s", rel.Version)
	}
}

func TestGitHubRejectsBadChecksumsSignature(t *testing.T) {
	_, pub, _ := sign.Generate()
	pubPEM, _ := sign.EncodePublicPEM(pub)
	dir := t.TempDir()
	pubPath := filepath.Join(dir, "key.pub")
	_ = os.WriteFile(pubPath, pubPEM, 0o644)
	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/acme/app/releases" {
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"tag_name": "v1.2.0", "published_at": "2026-01-01T00:00:00Z",
				"assets": []map[string]any{
					{"id": 1, "name": "app.zip", "browser_download_url": baseURL + "/app.zip", "digest": "sha256:" + strings.Repeat("c", 64)},
					{"id": 2, "name": "checksums.txt", "browser_download_url": baseURL + "/checksums.txt"},
					{"id": 3, "name": "checksums.txt.sig", "browser_download_url": baseURL + "/checksums.txt.sig"},
				},
			}})
			return
		}
		if r.URL.Path == "/checksums.txt" {
			_, _ = w.Write([]byte(strings.Repeat("c", 64) + "  app.zip\n"))
			return
		}
		_, _ = w.Write([]byte("not a signature"))
	}))
	defer server.Close()
	baseURL = server.URL
	cfg := writeCfg(t, dir, `{"type":"github","owner":"acme","repo":"app","api_base":"`+server.URL+`"}`,
		`,"security":{"public_key_file":"`+pubPath+`"}`)
	client := httpx.NewClient(5*time.Second, httpx.Policy{AllowInsecure: true})
	p, _ := New(cfg, client, t.TempDir())
	if _, err := p.Fetch(context.Background()); err == nil {
		t.Fatal("bad checksum signature was accepted")
	}
}
