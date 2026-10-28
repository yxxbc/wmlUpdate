package install

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yxxbc/wmlUpdate/internal/archive"
	"github.com/yxxbc/wmlUpdate/internal/config"
	"github.com/yxxbc/wmlUpdate/internal/event"
	"github.com/yxxbc/wmlUpdate/internal/platform"
	"github.com/yxxbc/wmlUpdate/internal/release"
	"github.com/yxxbc/wmlUpdate/internal/state"
)

func loadConfig(t *testing.T, target string, extra string) *config.Config {
	t.Helper()
	dir := filepath.Dir(target)
	path := filepath.Join(dir, "updater.json")
	body := `{
	  "app_id": "com.example.installtest",
	  "current_version": "1.0.0",
	  "source": {"type": "static", "manifest_url": "https://updates.example.test/manifest.json"},
	  "install": {"type": "directory", "path": "` + filepath.ToSlash(target) + `", "preserve": ["data/**"], "keep_backups": 1` + extra + `},
	  "state_dir": "` + filepath.ToSlash(filepath.Join(dir, "state")) + `"
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

func newArchive(t *testing.T, files map[string]string) string {
	t.Helper()
	src := t.TempDir()
	for name, body := range files {
		p := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dest := filepath.Join(t.TempDir(), "app.zip")
	if err := archive.Create(src, dest, "zip"); err != nil {
		t.Fatal(err)
	}
	return dest
}

func TestDirectoryInstallPreserveRollback(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "app")
	if err := os.MkdirAll(filepath.Join(target, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "data", "keep.txt"), []byte("user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := loadConfig(t, target, "")
	cfg.Install.Paths = nil
	artifact := newArchive(t, map[string]string{"app": "new binary", "new.txt": "new"})

	store, err := state.Open(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Load(cfg.AppID)
	if err != nil {
		t.Fatal(err)
	}
	mgr := &Manager{Store: store, Data: st, KeepBackups: 1, Sink: event.NopSink{}}
	plan, err := Prepare(cfg, platform.Current(), release.Asset{Name: "app.zip", Format: "zip"}, artifact, "2.0.0", "1.0.0", event.NopSink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "app")); string(got) != "new binary" {
		t.Fatalf("new binary = %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old file should be removed, err=%v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "data", "keep.txt")); string(got) != "user data" {
		t.Fatalf("preserved data = %q", got)
	}
	st, _ = store.Load(cfg.AppID)
	if st.InstalledVersion != "2.0.0" || len(st.Backups) != 1 {
		t.Fatalf("state = %+v", st)
	}
	if _, err := mgr.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "old.txt")); string(got) != "old" {
		t.Fatalf("rollback old.txt = %q", got)
	}
	st, _ = store.Load(cfg.AppID)
	if st.InstalledVersion != "1.0.0" {
		t.Fatalf("rolled back state version = %q", st.InstalledVersion)
	}
}

func TestFileInstallRollback(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "wml-update")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "updater.json")
	body := `{
	  "app_id": "com.example.filet",
	  "source": {"type": "static", "manifest_url": "https://updates.example.test/manifest.json"},
	  "install": {"type": "file", "path": "` + filepath.ToSlash(target) + `", "file_mode": "0755"},
	  "state_dir": "` + filepath.ToSlash(filepath.Join(root, "state")) + `"
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(root, "new.bin")
	if err := os.WriteFile(artifact, []byte("new binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, _ := state.Open(cfg.StateDir)
	st, _ := store.Load(cfg.AppID)
	mgr := &Manager{Store: store, Data: st, KeepBackups: 1, Sink: event.NopSink{}}
	plan, err := Prepare(cfg, platform.Current(), release.Asset{Name: "new.bin", Format: "raw"}, artifact, "2.0.0", "1.0.0", event.NopSink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "new binary" {
		t.Fatalf("file = %q", got)
	}
	if _, err := mgr.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old binary" {
		t.Fatalf("rollback file = %q", got)
	}
}

func TestPrepareRejectsRootTarget(t *testing.T) {
	cfg := &config.Config{
		AppID: "x",
		Install: config.InstallConfig{
			Type: "directory", Path: string(filepath.Separator), KeepBackups: 1,
		},
		StateDir: t.TempDir(),
	}
	artifact := newArchive(t, map[string]string{"app": "x"})
	if _, err := Prepare(cfg, platform.Current(), release.Asset{Name: "a.zip", Format: "zip"}, artifact, "2.0.0", "1.0.0", event.NopSink{}); err == nil {
		t.Fatal("root install target was accepted")
	}
}
