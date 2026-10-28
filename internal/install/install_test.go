package install

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestMacOSQuarantineRemoval(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS-only metadata test")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "app")
	if err := os.WriteFile(file, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/xattr", "-w", "com.apple.quarantine", "0081;test", file).CombinedOutput(); err != nil {
		t.Fatalf("set xattr: %v %s", err, out)
	}
	mgr := &Manager{Sink: event.NopSink{}}
	if err := mgr.applyMacOSPostInstall(context.Background(), &Plan{
		Target: dir,
		MacOS:  config.MacOSConfig{RemoveQuarantine: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("/usr/bin/xattr", "-p", "com.apple.quarantine", file).Run(); err == nil {
		t.Fatal("quarantine xattr still present")
	}
}

func TestMacOSAppBundleUpdateSigns(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS-only codesign test")
	}
	root := t.TempDir()
	target := filepath.Join(root, "MyApp.app")
	if err := os.MkdirAll(filepath.Join(target, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "Contents", "Info.plist"), []byte("<plist><dict><key>CFBundleExecutable</key><string>Test</string></dict></plist>"), 0o644); err != nil {
		t.Fatal(err)
	}
	echo, err := os.ReadFile("/bin/echo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "Contents", "MacOS", "Test"), echo, 0o755); err != nil {
		t.Fatal(err)
	}

	artifact := newArchive(t, map[string]string{
		"Contents/Info.plist": "<plist><dict><key>CFBundleExecutable</key><string>Test</string></dict></plist>",
		"Contents/MacOS/Test": string(echo),
	})
	cfg := loadConfig(t, target, `, "macos": {"remove_quarantine": true, "ad_hoc_sign": true}`)
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
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--deep", "--strict", target).CombinedOutput(); err != nil {
		t.Fatalf("codesign verify: %v\n%s", err, out)
	}
}

func TestMacOSSignerContinuity(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS-only signer test")
	}
	target := filepath.Join(t.TempDir(), "App.app")
	stage := filepath.Join(t.TempDir(), "stage")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	oldTeam, oldVerify := macCodeTeam, macVerify
	t.Cleanup(func() { macCodeTeam, macVerify = oldTeam, oldVerify })
	macVerify = func(context.Context, string) error { return nil }

	macCodeTeam = func(_ context.Context, app string) string {
		if app == target {
			return "OLD TEAM"
		}
		return "NEW TEAM"
	}
	if err := verifyMacOSSigner(context.Background(), target, stage, config.MacOSConfig{}); err == nil {
		t.Fatal("different signing team was accepted")
	}
	if err := verifyMacOSSigner(context.Background(), target, stage, config.MacOSConfig{AllowSignerChange: true}); err != nil {
		t.Fatalf("allow_signer_change should permit team change: %v", err)
	}
	if err := verifyMacOSSigner(context.Background(), target, stage, config.MacOSConfig{AdHocSign: true}); err != nil {
		t.Fatalf("ad_hoc_sign should bypass team continuity: %v", err)
	}

	macCodeTeam = func(_ context.Context, app string) string {
		if app == target {
			return "OLD TEAM"
		}
		return "OLD TEAM"
	}
	if err := verifyMacOSSigner(context.Background(), target, stage, config.MacOSConfig{}); err != nil {
		t.Fatalf("same team rejected: %v", err)
	}

	macCodeTeam = func(context.Context, string) string { return "" }
	if err := verifyMacOSSigner(context.Background(), target, stage, config.MacOSConfig{}); err != nil {
		t.Fatalf("unsigned installed app should skip continuity: %v", err)
	}
}
