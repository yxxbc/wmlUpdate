package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func makeZip(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range entries {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if strings.HasSuffix(name, "/") {
			h.SetMode(os.ModeDir | 0o755)
		} else {
			h.SetMode(0o644)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func makeTarGz(t *testing.T, entries []tar.Header, bodies map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for i := range entries {
		h := entries[i]
		if body, ok := bodies[h.Name]; ok {
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if body, ok := bodies[h.Name]; ok {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractZipAndStrip(t *testing.T) {
	path := makeZip(t, map[string]string{
		"bundle/":           "",
		"bundle/app":        "binary",
		"bundle/config/a":   "a",
		"bundle/config/b/":  "",
		"bundle/config/b/c": "c",
	})
	dest := filepath.Join(t.TempDir(), "out")
	if err := Extract(path, dest, "zip", ExtractOptions{StripComponents: 1, AllowSymlinks: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "app"))
	if err != nil || string(data) != "binary" {
		t.Fatalf("app = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "config", "b", "c")); err != nil {
		t.Fatal(err)
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	for _, name := range []string{"../evil", "..\\evil", "/evil", "C:/evil", "//server/share"} {
		path := makeZip(t, map[string]string{name: "bad"})
		err := Extract(path, filepath.Join(t.TempDir(), "out"), "zip", ExtractOptions{AllowSymlinks: true})
		if err == nil {
			t.Errorf("entry %q unexpectedly extracted", name)
		}
	}
}

func TestExtractRejectsEscapingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink extraction requires privileges on Windows")
	}
	path := filepath.Join(t.TempDir(), "link.zip")
	f, _ := os.Create(path)
	zw := zip.NewWriter(f)
	h := &zip.FileHeader{Name: "link"}
	h.SetMode(os.ModeSymlink | 0o777)
	w, _ := zw.CreateHeader(h)
	_, _ = w.Write([]byte("../../etc"))
	_ = zw.Close()
	_ = f.Close()
	if err := Extract(path, filepath.Join(t.TempDir(), "out"), "zip", ExtractOptions{AllowSymlinks: true}); err == nil {
		t.Fatal("escaping symlink was accepted")
	}
}

func TestExtractRejectsSymlinkParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink extraction requires privileges on Windows")
	}
	entries := []tar.Header{
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "sub", Mode: 0o777},
		{Name: "link/file", Typeflag: tar.TypeReg, Mode: 0o644},
	}
	path := makeTarGz(t, entries, map[string]string{"link/file": "bad"})
	if err := Extract(path, filepath.Join(t.TempDir(), "out"), "tar.gz", ExtractOptions{AllowSymlinks: true}); err == nil {
		t.Fatal("write through a symlink parent was accepted")
	}
}

func TestExtractTarTraversal(t *testing.T) {
	entries := []tar.Header{{Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o644}}
	path := makeTarGz(t, entries, map[string]string{"../evil": "bad"})
	if err := Extract(path, filepath.Join(t.TempDir(), "out"), "tar.gz", ExtractOptions{AllowSymlinks: true}); err == nil {
		t.Fatal("tar traversal was accepted")
	}
}

func TestMaxEntries(t *testing.T) {
	entries := map[string]string{"a": "1", "b": "2", "c": "3"}
	path := makeZip(t, entries)
	if err := Extract(path, filepath.Join(t.TempDir(), "out"), "zip", ExtractOptions{AllowSymlinks: true, MaxFiles: 2}); err == nil {
		t.Fatal("expected max entries error")
	}
}

func TestCreateRoundTrip(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "file"), []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink("sub/file", filepath.Join(src, "link")); err != nil {
			t.Fatal(err)
		}
	}
	for _, format := range []string{"zip", "tar.gz", "tar"} {
		t.Run(format, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "archive")
			if err := Create(src, dest, format); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "out")
			if err := Extract(dest, out, format, ExtractOptions{AllowSymlinks: true}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(out, "sub", "file"))
			if err != nil || string(data) != "hello" {
				t.Fatalf("file = %q, %v", data, err)
			}
			if runtime.GOOS != "windows" {
				target, err := os.Readlink(filepath.Join(out, "link"))
				if err != nil || target != "sub/file" {
					t.Fatalf("link = %q, %v", target, err)
				}
			}
		})
	}
}

func TestDetectFormat(t *testing.T) {
	zipPath := makeZip(t, map[string]string{"a": "b"})
	if got := DetectFormat(zipPath); got != "zip" {
		t.Errorf("DetectFormat(zip) = %q", got)
	}
	tarPath := makeTarGz(t, []tar.Header{{Name: "a", Typeflag: tar.TypeReg, Mode: 0o644}}, map[string]string{"a": "b"})
	if got := DetectFormat(tarPath); got != "tar.gz" {
		t.Errorf("DetectFormat(tar.gz) = %q", got)
	}
	raw := filepath.Join(t.TempDir(), "thing.bin")
	_ = os.WriteFile(raw, []byte("plain"), 0o600)
	if got := DetectFormat(raw); got != "" {
		t.Errorf("DetectFormat(raw) = %q", got)
	}
}
