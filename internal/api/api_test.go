package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yxxbc/wmlUpdate/internal/config"
	"github.com/yxxbc/wmlUpdate/internal/manifest"
	"github.com/yxxbc/wmlUpdate/internal/platform"
)

func apiFixture(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	payload := []byte("binary payload")
	sum := sha256.Sum256(payload)
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest.json" {
			m := &manifest.Manifest{
				SchemaVersion: 1, AppID: "com.example.api", Channel: "stable", Version: "2.0.0",
				Assets: []manifest.Asset{{
					Name: "app.bin", OS: platform.Current().OS, Arch: platform.Current().Arch,
					URL: "app.bin", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(payload)), Format: "raw",
				}},
			}
			raw, _ := manifest.Marshal(m)
			_, _ = w.Write(raw)
			return
		}
		_, _ = w.Write(payload)
	}))
	t.Cleanup(src.Close)
	cfgPath := filepath.Join(dir, "updater.json")
	body := `{
	  "app_id": "com.example.api",
	  "channel": "stable",
	  "current_version": "1.0.0",
	  "source": {"type": "static", "manifest_url": "` + src.URL + `/manifest.json"},
	  "install": {"type": "external"},
	  "state_dir": "` + filepath.ToSlash(filepath.Join(dir, "state")) + `"
	}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, platform.Current(), Options{Token: "0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	return srv, "0123456789abcdef"
}

func request(t *testing.T, client *http.Client, method, url, token string, body any) *http.Response {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAPIAuthAndCheck(t *testing.T) {
	srv, token := apiFixture(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	client := ts.Client()

	resp := request(t, client, "GET", ts.URL+"/v1/health", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("health = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = request(t, client, "POST", ts.URL+"/v1/check", "", map[string]string{})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = request(t, client, "POST", ts.URL+"/v1/check", token, map[string]string{"current_version": "1.0.0"})
	if resp.StatusCode != 200 {
		t.Fatalf("check status = %d", resp.StatusCode)
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if result["status"] != "update_available" || result["latest_version"] != "2.0.0" {
		t.Fatalf("check = %+v", result)
	}
}

func TestAPIUpdateJob(t *testing.T) {
	srv, token := apiFixture(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	client := ts.Client()

	resp := request(t, client, "POST", ts.URL+"/v1/update", token, map[string]any{"current_version": "1.0.0"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("update status = %d", resp.StatusCode)
	}
	var job map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&job)
	resp.Body.Close()
	id, _ := job["id"].(string)
	if id == "" {
		t.Fatalf("job = %+v", job)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp = request(t, client, "GET", ts.URL+"/v1/jobs/"+id, token, nil)
		var snap struct {
			Status string         `json:"status"`
			Result map[string]any `json:"result"`
			Error  map[string]any `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&snap)
		resp.Body.Close()
		if snap.Status != "running" {
			if snap.Status != "succeeded" {
				t.Fatalf("job failed: %+v", snap)
			}
			if snap.Result["status"] != "downloaded" {
				t.Fatalf("job result = %+v", snap.Result)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
