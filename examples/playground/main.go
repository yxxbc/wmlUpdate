// Command playground runs a complete local update without any external service.
//
// It creates a temporary v1 application, a v2 zip, an unsigned local manifest,
// a loopback HTTP server and a tiny Go consumer that uses the public SDK:
//
//	go run ./examples/playground
//	go run ./examples/playground -keep
//
// This is a development harness, not a release example. Production updates
// should use Ed25519-signed manifests or signed checksums.
package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"time"

	wmlupdate "github.com/yxxbc/wmlUpdate"
)

type manifestAsset struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Format string `json:"format"`
}

type manifestFile struct {
	SchemaVersion int             `json:"schema_version"`
	AppID         string          `json:"app_id"`
	Channel       string          `json:"channel"`
	Version       string          `json:"version"`
	Notes         string          `json:"notes"`
	Assets        []manifestAsset `json:"assets"`
}

func main() {
	keep := flag.Bool("keep", false, "keep the temporary playground directory")
	flag.Parse()

	root, err := os.MkdirTemp("", "wml-update-playground-*")
	if err != nil {
		fatal(err)
	}
	if !*keep {
		defer os.RemoveAll(root)
	}

	target := filepath.Join(root, "app")
	if err := os.MkdirAll(target, 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "app"), []byte("version 1\n"), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "VERSION"), []byte("1.0.0\n"), 0o644); err != nil {
		fatal(err)
	}

	buildDir := filepath.Join(root, "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "app"), []byte("version 2\n"), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "VERSION"), []byte("2.0.0\n"), 0o644); err != nil {
		fatal(err)
	}
	archivePath := filepath.Join(root, "app.zip")
	if err := createZip(buildDir, archivePath); err != nil {
		fatal(err)
	}
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		fatal(err)
	}
	sum := sha256.Sum256(archiveBytes)
	digest := hex.EncodeToString(sum[:])

	manifest := manifestFile{
		SchemaVersion: 1,
		AppID:         "com.example.playground",
		Channel:       "stable",
		Version:       "2.0.0",
		Notes:         "Playground update from a loopback server.",
		Assets: []manifestAsset{{
			Name:   "app.zip",
			OS:     runtime.GOOS,
			Arch:   runtime.GOARCH,
			URL:    "app.zip",
			SHA256: digest,
			Size:   int64(len(archiveBytes)),
			Format: "zip",
		}},
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(append(manifestBytes, '\n'))
		case "/app.zip":
			_, _ = w.Write(archiveBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fmt.Printf("playground directory: %s\n", root)
	fmt.Printf("target:               %s\n", target)
	fmt.Printf("update source:        %s/manifest.json\n", server.URL)

	cfg := &wmlupdate.Config{
		AppID:   "com.example.playground",
		Channel: "stable",
		Source: wmlupdate.SourceConfig{
			Type:        wmlupdate.SourceStatic,
			ManifestURL: server.URL + "/manifest.json",
		},
		AssetRules: []wmlupdate.AssetRule{{
			OS: runtime.GOOS, Arch: runtime.GOARCH, Match: "app.zip", Format: "zip",
		}},
		Install: wmlupdate.InstallConfig{
			Type:        wmlupdate.InstallDirectory,
			Path:        target,
			KeepBackups: 1,
		},
		StateDir: filepath.Join(root, "state"),
	}
	u, err := wmlupdate.New(wmlupdate.Options{
		Config:         cfg,
		BaseDir:        root,
		CurrentVersion: "1.0.0",
		Events: func(e wmlupdate.Event) {
			if e.Type == wmlupdate.EventProgress && e.Phase == "download" {
				fmt.Printf("download: %.0f%%\n", e.Percent)
			}
		},
	})
	if err != nil {
		fatal(err)
	}
	defer u.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	update, err := u.Check(ctx)
	if err != nil {
		fatalf("check failed [%s]: %v", wmlupdate.Code(err), err)
	}
	if !update.Available {
		fatal("expected an available update")
	}
	fmt.Printf("check: %s -> %s (%s)\n", update.CurrentVersion, update.Version, update.Asset.Name)

	result, err := u.Apply(ctx, update, wmlupdate.ApplyOptions{})
	if err != nil {
		fatalf("apply failed [%s]: %v", wmlupdate.Code(err), err)
	}
	if result.Status != wmlupdate.StatusUpdated {
		fatalf("unexpected apply status %q", result.Status)
	}
	installed, err := os.ReadFile(filepath.Join(target, "app"))
	if err != nil {
		fatal(err)
	}
	if string(installed) != "version 2\n" {
		fatalf("unexpected installed content: %q", installed)
	}
	fmt.Printf("apply:  %s -> %s (%s)\n", result.PreviousVersion, result.Version, result.Status)
	fmt.Printf("installed content: %s", installed)

	again, err := u.Check(ctx)
	if err != nil {
		fatal(err)
	}
	if again.Available {
		fatal("update still appears available after apply")
	}
	fmt.Println("result: local SDK end-to-end test passed")
	if *keep {
		fmt.Printf("kept directory: %s\n", root)
	}
}

func createZip(root, dest string) error {
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			h.Name += "/"
			_, err = zw.CreateHeader(h)
			return err
		}
		h.Method = zip.Deflate
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(w, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		_ = zw.Close()
		_ = out.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func fatal(v any) {
	fmt.Fprintln(os.Stderr, "playground:", v)
	os.Exit(1)
}

func fatalf(format string, args ...any) {
	fatal(fmt.Sprintf(format, args...))
}
