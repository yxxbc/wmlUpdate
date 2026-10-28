package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yxxbc/wmlUpdate/internal/archive"
	"github.com/yxxbc/wmlUpdate/internal/checksum"
	"github.com/yxxbc/wmlUpdate/internal/config"
	"github.com/yxxbc/wmlUpdate/internal/event"
	"github.com/yxxbc/wmlUpdate/internal/install"
	"github.com/yxxbc/wmlUpdate/internal/manifest"
	"github.com/yxxbc/wmlUpdate/internal/platform"
	"github.com/yxxbc/wmlUpdate/internal/release"
	"github.com/yxxbc/wmlUpdate/internal/sign"
	"github.com/yxxbc/wmlUpdate/internal/state"
)

type fixture struct {
	dir      string
	target   string
	cfg      *config.Config
	p        platform.Platform
	zipBytes []byte
	server   *httptest.Server
	sink     *event.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "app")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "app"), []byte("version one"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "build")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "app"), []byte("version two"), 0o755); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(dir, "app.zip")
	if err := archive.Create(work, zipPath, "zip"); err != nil {
		t.Fatal(err)
	}
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(zipBytes)

	priv, pub, err := sign.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, _ := sign.EncodePublicPEM(pub)
	pubPath := filepath.Join(dir, "key.pub")
	if err := os.WriteFile(pubPath, pubPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	p := platform.Current()
	m := &manifest.Manifest{
		SchemaVersion: manifest.SchemaVersion,
		AppID:         "com.example.engine",
		Channel:       "stable",
		Version:       "2.0.0",
		PublishedAt:   time.Now().UTC().Format(time.RFC3339),
		Notes:         "engine test",
		Assets: []manifest.Asset{{
			Name: "app.zip", OS: p.OS, Arch: p.Arch, URL: "app.zip",
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(zipBytes)), Format: "zip",
		}},
	}
	raw, err := manifest.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	sig := sign.Sign(priv, raw)
	fx := &fixture{dir: dir, target: target, p: p, zipBytes: zipBytes, sink: event.NewBuffer(256)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write(raw)
		case "/manifest.json.sig":
			_, _ = w.Write([]byte(sig + "\n"))
		case "/app.zip":
			_, _ = w.Write(fx.zipBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	fx.server = server
	t.Cleanup(server.Close)

	cfgPath := filepath.Join(dir, "updater.json")
	cfgBody := `{
	  "app_id": "com.example.engine",
	  "channel": "stable",
	  "current_version": "1.0.0",
	  "source": {"type": "static", "manifest_url": "` + server.URL + `/manifest.json"},
	  "asset_rules": [{"os": "` + p.OS + `", "arch": "` + p.Arch + `", "match": "app.zip", "format": "zip"}],
	  "install": {"type": "directory", "path": "` + target + `", "preserve": ["data/**"], "keep_backups": 1},
	  "state_dir": "` + filepath.Join(dir, "state") + `",
	  "security": {"public_key_file": "` + pubPath + `"}
	}`
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	fx.cfg = cfg
	return fx
}

func (f *fixture) engine(t *testing.T) *Engine {
	t.Helper()
	eng, err := New(f.cfg, f.p, f.sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	return eng
}

func TestEngineCheckFetchUpdateRollback(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	eng := fx.engine(t)

	check, err := eng.Check(ctx, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if check.Status != StatusUpdateAvailable || check.LatestVersion != "2.0.0" {
		t.Fatalf("check = %+v", check)
	}

	output := filepath.Join(fx.dir, "fetched.zip")
	fetched, err := eng.Fetch(ctx, FetchOptions{CurrentVersion: "1.0.0", Output: output})
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Status != StatusDownloaded || fetched.ArtifactPath != output {
		t.Fatalf("fetch = %+v", fetched)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatal(err)
	}

	res, err := eng.Update(ctx, UpdateOptions{CurrentVersion: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusUpdated || res.CurrentVersion != "2.0.0" || res.PreviousVersion != "1.0.0" {
		t.Fatalf("update = %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(fx.target, "app")); string(got) != "version two" {
		t.Fatalf("installed app = %q", got)
	}
	st, err := eng.Status()
	if err != nil || st.InstalledVersion != "2.0.0" || len(st.Backups) != 1 {
		t.Fatalf("state = %+v, %v", st, err)
	}

	after, err := eng.Check(ctx, "2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusUpToDate {
		t.Fatalf("after update = %+v", after)
	}

	rb, err := eng.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rb.Status != StatusRolledBack || rb.CurrentVersion != "1.0.0" {
		t.Fatalf("rollback = %+v", rb)
	}
	if got, _ := os.ReadFile(filepath.Join(fx.target, "app")); string(got) != "version one" {
		t.Fatalf("rolled back app = %q", got)
	}
}

func TestEngineRejectsCorruptArtifact(t *testing.T) {
	fx := newFixture(t)
	fx.zipBytes = []byte("this is not the signed artifact")
	eng := fx.engine(t)
	_, err := eng.Update(context.Background(), UpdateOptions{CurrentVersion: "1.0.0"})
	if err == nil {
		t.Fatal("corrupt artifact was installed")
	}
	if CodeOf(err) != "integrity" {
		t.Fatalf("error code = %s, err = %v", CodeOf(err), err)
	}
	if got, _ := os.ReadFile(filepath.Join(fx.target, "app")); string(got) != "version one" {
		t.Fatalf("target was modified: %q", got)
	}
}

func TestRunHelperAppliesPlan(t *testing.T) {
	fx := newFixture(t)
	zipPath := filepath.Join(fx.dir, "app.zip")
	if err := os.WriteFile(zipPath, fx.zipBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := release.Asset{Name: "app.zip", Format: "zip", SHA256: strings.Repeat("a", 64)}
	plan, err := install.Prepare(fx.cfg, fx.p, artifact, zipPath, "2.0.0", "1.0.0", event.NopSink{})
	if err != nil {
		t.Fatal(err)
	}
	path := install.PlanPath(fx.cfg.StateDir, plan.ID)
	if err := install.WritePlan(path, plan); err != nil {
		t.Fatal(err)
	}
	if err := RunHelper(context.Background(), path, nil, 0, event.NopSink{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(fx.target, "app")); string(got) != "version two" {
		t.Fatalf("helper install = %q", got)
	}
	store, _ := state.Open(fx.cfg.StateDir)
	st, _ := store.Load(fx.cfg.AppID)
	if st.InstalledVersion != "2.0.0" {
		t.Fatalf("state version = %q", st.InstalledVersion)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("plan should be removed: %v", err)
	}
	result, err := os.ReadFile(HelperResultPath(fx.cfg.StateDir, plan.ID))
	if err != nil || !strings.Contains(string(result), `"ok": true`) {
		t.Fatalf("helper result = %s, %v", result, err)
	}
}

func TestChecksumFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(path, []byte("abc"), 0o600)
	digest, size, err := checksum.File(path)
	if err != nil || size != 3 || len(digest) != 64 {
		t.Fatalf("checksum = %s %d %v", digest, size, err)
	}
}
