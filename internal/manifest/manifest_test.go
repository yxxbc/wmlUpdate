package manifest

import (
	"strings"
	"testing"

	"github.com/yxxbc/wmlUpdate/internal/checksum"
	"github.com/yxxbc/wmlUpdate/internal/sign"
)

func testManifest() *Manifest {
	return &Manifest{
		SchemaVersion: SchemaVersion,
		AppID:         "com.example.app",
		Channel:       "stable",
		Version:       "1.2.3",
		PublishedAt:   "2026-01-02T03:04:05Z",
		Notes:         "fixes",
		Assets: []Asset{{
			Name:   "app-linux-amd64.tar.gz",
			OS:     "linux",
			Arch:   "amd64",
			URL:    "app-linux-amd64.tar.gz",
			SHA256: strings.Repeat("a", 64),
			Size:   123,
			Format: "tar.gz",
		}},
	}
}

func TestMarshalParseAndValidate(t *testing.T) {
	m := testManifest()
	raw, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.AppID != m.AppID || parsed.Assets[0].SHA256 != m.Assets[0].SHA256 {
		t.Fatalf("parsed manifest differs: %+v", parsed)
	}
}

func TestSignature(t *testing.T) {
	priv, pub, err := sign.Generate()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Marshal(testManifest())
	if err != nil {
		t.Fatal(err)
	}
	sig := Sign(raw, priv)
	if err := Verify(raw, sig, pub); err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)-3] ^= 1
	if err := Verify(tampered, sig, pub); err == nil {
		t.Fatal("tampered manifest signature verified")
	}
}

func TestChecksums(t *testing.T) {
	m := testManifest()
	m.Assets = append(m.Assets, Asset{Name: "app-windows-amd64.zip", URL: "app.zip", SHA256: strings.Repeat("b", 64)})
	data := Checksums(m)
	sums, err := checksum.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := sums["app-windows-amd64.zip"]; got != strings.Repeat("b", 64) {
		t.Fatalf("checksum = %q", got)
	}
	if !strings.HasPrefix(string(data), strings.Repeat("a", 64)+"  app-linux-amd64.tar.gz") {
		t.Fatalf("checksums not sorted/formatted: %s", data)
	}
}

func TestValidateRejectsBadHash(t *testing.T) {
	m := testManifest()
	m.Assets[0].SHA256 = "not-a-hash"
	if err := m.Validate(); err == nil {
		t.Fatal("expected invalid hash error")
	}
}

func TestSignatureURL(t *testing.T) {
	got, err := SignatureURL("https://example.test/stable/manifest.json?x=1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://example.test/stable/manifest.json.sig?x=1"; got != want {
		t.Fatalf("SignatureURL = %q, want %q", got, want)
	}
}
