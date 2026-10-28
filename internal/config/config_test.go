package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yxxbc/wmlUpdate/internal/platform"
)

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "updater.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAndDefaults(t *testing.T) {
	t.Setenv("TEST_UPDATE_TOKEN", "secret-token")
	dir := t.TempDir()
	installDir := filepath.Join(dir, "app")
	path := writeConfig(t, dir, `{
	  "$schema": "https://raw.githubusercontent.com/yxxbc/wmlUpdate/main/schema/config.schema.json",
	  "schema_version": 1,
	  "app_id": "com.example.app",
	  "channel": "stable",
	  "current_version": "1.2.3",
	  "source": {
	    "type": "static",
	    "manifest_url": "https://updates.example.com/manifest.json",
	    "headers": {"Authorization": "Bearer ${TEST_UPDATE_TOKEN}"}
	  },
	  "asset_rules": [
	    {"os": "macOS", "arch": "arm64", "match": "app-{version}-macos-arm64.zip", "format": "zip"}
	  ],
	  "install": {"type": "directory", "path": "app", "preserve": ["data/**"]},
	  "state_dir": "state"
	}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StateDir != filepath.Join(dir, "state") {
		t.Errorf("StateDir = %q", cfg.StateDir)
	}
	if cfg.Source.Headers["Authorization"] != "Bearer secret-token" {
		t.Errorf("header was not expanded: %q", cfg.Source.Headers["Authorization"])
	}
	if got, _ := cfg.ResolveInstallPath(platformForTest()); got != installDir {
		t.Errorf("install path = %q, want %q", got, installDir)
	}
	if cfg.Download.MaxBytes != 2<<30 || cfg.Download.Retries != 3 {
		t.Errorf("download defaults not applied: %+v", cfg.Download)
	}
	if cfg.Security.ChecksumsAsset != "checksums.txt" {
		t.Errorf("checksums default = %q", cfg.Security.ChecksumsAsset)
	}
	if cfg.Install.KeepBackups != 1 {
		t.Errorf("keep backups default = %d", cfg.Install.KeepBackups)
	}
}

func platformForTest() platform.Platform {
	// Current() is what the updater uses; tests run on a supported host.
	return platform.Current()
}

func TestLoadRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `{"app_id":"x","source":{"type":"static","manifest_url":"https://x.test/m.json"},"install":{"type":"external"},"unknown":true}`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestLoadRejectsUnsetEnv(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `{
	  "app_id":"x",
	  "source":{"type":"static","manifest_url":"https://x.test/m.json","headers":{"Authorization":"Bearer ${DEFINITELY_UNSET_12345}"}},
	  "install":{"type":"external"}
	}`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "DEFINITELY_UNSET_12345") {
		t.Fatalf("expected missing env error, got %v", err)
	}
}

func TestLoadChainMirrorsProxyAndMacOS(t *testing.T) {
	t.Setenv("TEST_PROXY", "http://127.0.0.1:7890")
	dir := t.TempDir()
	path := writeConfig(t, dir, `{
	  "app_id": "com.example.chain",
	  "source": {
	    "type": "chain",
	    "sources": [
	      {"type": "github", "owner": "acme", "repo": "app", "asset_mirrors": ["https://ghproxy.net/{url}"]},
	      {"type": "static", "manifest_urls": ["https://a.example/manifest.json", "https://b.example/manifest.json"]}
	    ]
	  },
	  "install": {
	    "type": "directory",
	    "path": "app",
	    "macos": {"remove_quarantine": true, "ad_hoc_sign": true}
	  },
	  "download": {"proxy": "${TEST_PROXY}"}
	}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source.Type != "chain" || len(cfg.Source.Sources) != 2 {
		t.Fatalf("source = %+v", cfg.Source)
	}
	if len(cfg.Source.Sources[1].ManifestURLs) != 2 || cfg.Source.Sources[1].ManifestURL != "https://a.example/manifest.json" {
		t.Fatalf("static mirrors = %+v", cfg.Source.Sources[1])
	}
	if len(cfg.Source.Sources[0].AssetMirrors) != 1 {
		t.Fatalf("asset mirrors = %+v", cfg.Source.Sources[0].AssetMirrors)
	}
	if cfg.Download.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("proxy = %q", cfg.Download.Proxy)
	}
	if !cfg.Install.MacOS.RemoveQuarantine || !cfg.Install.MacOS.AdHocSign {
		t.Fatalf("macos config = %+v", cfg.Install.MacOS)
	}
}

func TestLoadRejectsInvalidChain(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `{
	  "app_id": "com.example.chain",
	  "source": {"type": "chain", "sources": [{"type": "chain", "sources": []}]},
	  "install": {"type": "external"}
	}`)
	if _, err := Load(path); err == nil {
		t.Fatal("nested chain was accepted")
	}
}
