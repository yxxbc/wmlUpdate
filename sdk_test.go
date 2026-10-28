package wmlupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yxxbc/wmlUpdate/internal/archive"
	"github.com/yxxbc/wmlUpdate/internal/manifest"
	"github.com/yxxbc/wmlUpdate/internal/platform"
)

func sdkFixture(t *testing.T) (string, string, *httptest.Server, []byte) {
	t.Helper()
	root := t.TempDir()
	target := filepath.Join(root, "app")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "app"), []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	build := filepath.Join(root, "build")
	if err := os.MkdirAll(build, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(build, "app"), []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(root, "app.zip")
	if err := archive.Create(build, zipPath, "zip"); err != nil {
		t.Fatal(err)
	}
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(zipBytes)
	p := platform.Current()
	m := &manifest.Manifest{
		SchemaVersion: manifest.SchemaVersion,
		AppID:         "com.example.sdk",
		Channel:       "stable",
		Version:       "2.0.0",
		PublishedAt:   time.Now().UTC().Format(time.RFC3339),
		Assets: []manifest.Asset{{
			Name: "app.zip", OS: p.OS, Arch: p.Arch, URL: "app.zip",
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(zipBytes)), Format: "zip",
		}},
	}
	raw, err := manifest.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write(raw)
		case "/app.zip":
			_, _ = w.Write(zipBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return root, target, server, raw
}

func TestSDKCheckAndApply(t *testing.T) {
	root, target, server, _ := sdkFixture(t)
	var events []Event
	cfg := &Config{
		AppID:   "com.example.sdk",
		Channel: "stable",
		Source: SourceConfig{
			Type:        SourceStatic,
			ManifestURL: server.URL + "/manifest.json",
		},
		AssetRules: []AssetRule{{OS: platform.Current().OS, Arch: platform.Current().Arch, Match: "app.zip", Format: "zip"}},
		Install: InstallConfig{
			Type: InstallDirectory, Path: target, KeepBackups: 1,
		},
		StateDir: filepath.Join(root, "state"),
	}
	u, err := New(Options{
		Config:         cfg,
		BaseDir:        root,
		CurrentVersion: "1.0.0",
		Events:         func(e Event) { events = append(events, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()

	ctx := context.Background()
	update, err := u.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !update.Available || update.Version != "2.0.0" || update.CurrentVersion != "1.0.0" {
		t.Fatalf("update = %+v", update)
	}
	if update.Asset.SHA256 == "" || update.Asset.Name != "app.zip" {
		t.Fatalf("asset = %+v", update.Asset)
	}
	res, err := u.Apply(ctx, update, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "updated" || res.Version != "2.0.0" || res.PreviousVersion != "1.0.0" {
		t.Fatalf("result = %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "app")); string(got) != "v2" {
		t.Fatalf("installed = %q", got)
	}
	if len(events) == 0 {
		t.Fatal("no progress/log events were delivered")
	}

	again, err := u.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Available {
		t.Fatalf("update still available after apply: %+v", again)
	}
	if _, err := u.Apply(ctx, again, ApplyOptions{}); err == nil || Code(err) != "no_update" {
		t.Fatalf("expected no_update, got %v (%s)", err, Code(err))
	}
}

func TestSDKConfigPath(t *testing.T) {
	root, target, server, _ := sdkFixture(t)
	cfgPath := filepath.Join(root, "updater.json")
	body := map[string]any{
		"schema_version":  1,
		"app_id":          "com.example.sdk",
		"channel":         "stable",
		"current_version": "1.0.0",
		"source":          map[string]any{"type": "static", "manifest_url": server.URL + "/manifest.json"},
		"install":         map[string]any{"type": "directory", "path": target},
		"state_dir":       filepath.Join(root, "state"),
	}
	data, _ := json.Marshal(body)
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	u, err := New(Options{ConfigPath: cfgPath})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	update, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !update.Available || update.Version != "2.0.0" {
		t.Fatalf("update = %+v", update)
	}
}

func TestSDKRequiresOneConfig(t *testing.T) {
	if _, err := New(Options{}); err == nil || Code(err) != "config" {
		t.Fatalf("empty options error = %v", err)
	}
	if _, err := New(Options{ConfigPath: "x", Config: &Config{}}); err == nil || Code(err) != "config" {
		t.Fatalf("both configs error = %v", err)
	}
}
