package publish

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yxxbc/wmlUpdate/internal/checksum"
	"github.com/yxxbc/wmlUpdate/internal/manifest"
	"github.com/yxxbc/wmlUpdate/internal/sign"
)

func TestPublishSignedManifest(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "build")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "app"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	priv, pub, err := sign.Generate()
	if err != nil {
		t.Fatal(err)
	}
	privPEM, _ := sign.EncodePrivatePEM(priv)
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(keyPath, privPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(dir, "publish.json")
	spec := `{
	  "app_id": "com.example.publish",
	  "version": "1.2.3",
	  "channel": "stable",
	  "notes": "test release",
	  "assets": [
	    {"dir": "build", "url": "com.example.publish-1.2.3-linux-amd64.tar.gz", "os": "Linux", "arch": "x86_64", "format": "tar.gz"}
	  ]
	}`
	if err := os.WriteFile(specPath, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	res, err := Publish(specPath, Options{OutDir: out, SigningKey: keyPath, BaseURL: "https://updates.example.com/stable"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(res.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.AppID != "com.example.publish" || m.Version != "1.2.3" || len(m.Assets) != 1 {
		t.Fatalf("manifest = %+v", m)
	}
	if m.Assets[0].OS != "linux" || m.Assets[0].Arch != "amd64" {
		t.Fatalf("normalized platform = %+v", m.Assets[0])
	}
	if m.Assets[0].URL != "https://updates.example.com/stable/com.example.publish-1.2.3-linux-amd64.tar.gz" {
		t.Fatalf("asset URL = %q", m.Assets[0].URL)
	}
	sig, err := os.ReadFile(res.SignaturePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Verify(raw, string(sig), pub); err != nil {
		t.Fatal(err)
	}
	sums, err := os.ReadFile(res.ChecksumsPath)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := checksum.Parse(sums)
	if err != nil {
		t.Fatal(err)
	}
	if parsed[m.Assets[0].Name] != m.Assets[0].SHA256 {
		t.Fatalf("checksums = %v", parsed)
	}
	if _, err := os.Stat(filepath.Join(out, filepath.Base(m.Assets[0].URL))); err != nil {
		t.Fatal(err)
	}
}

func TestSpecRejectsEscapingURL(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "app"), []byte("x"), 0o644)
	spec := `{"app_id":"x","version":"1.0.0","assets":[{"path":"app","url":"../escape"}]}`
	specPath := filepath.Join(dir, "spec.json")
	_ = os.WriteFile(specPath, []byte(spec), 0o600)
	if _, err := Publish(specPath, Options{OutDir: filepath.Join(dir, "out")}); err == nil {
		t.Fatal("escaping asset URL was accepted")
	}
}
