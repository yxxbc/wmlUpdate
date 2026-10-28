package httpx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw       string
		allowHTTP bool
		hosts     []string
		ok        bool
	}{
		{"https://example.com/a", false, nil, true},
		{"http://example.com/a", false, nil, false},
		{"http://example.com/a", true, nil, true},
		{"http://127.0.0.1:8080/a", false, nil, true},
		{"https://api.example.com/a", false, []string{"api.example.com"}, true},
		{"https://cdn.example.com/a", false, []string{"api.example.com"}, false},
		{"https://a.example.com/a", false, []string{"*.example.com"}, true},
		{"https://example.com.evil/a", false, []string{"*.example.com"}, false},
	}
	for _, tc := range cases {
		u, _ := url.Parse(tc.raw)
		err := CheckURL(u, Policy{AllowInsecure: tc.allowHTTP, AllowedHosts: tc.hosts})
		if (err == nil) != tc.ok {
			t.Errorf("CheckURL(%s, allow=%v, hosts=%v) error=%v, want ok=%v", tc.raw, tc.allowHTTP, tc.hosts, err, tc.ok)
		}
	}
}

func TestDownloadAndResume(t *testing.T) {
	content := []byte(strings.Repeat("verified artifact data ", 1000))
	sum := sha256.Sum256(content)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "app.bin", time.Now(), strings.NewReader(string(content)))
	}))
	defer server.Close()
	client := NewClient(10*time.Second, Policy{})
	dest := filepath.Join(t.TempDir(), "app.bin")
	part := dest + ".part"
	if err := os.WriteFile(part, content[:100], 0o600); err != nil {
		t.Fatal(err)
	}
	progress := int64(0)
	n, err := Download(context.Background(), client, DownloadOptions{
		URL:            server.URL + "/app.bin",
		Dest:           dest,
		ExpectedSHA256: hex.EncodeToString(sum[:]),
		ExpectedSize:   int64(len(content)),
		Retries:        1,
		Progress:       func(current, total int64) { progress = current },
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(content)) || progress != int64(len(content)) {
		t.Fatalf("n=%d progress=%d want %d", n, progress, len(content))
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(content) {
		t.Fatalf("downloaded content mismatch: %v", err)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("part file should be gone: %v", err)
	}
}

func TestDownloadRejectsBadChecksum(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("corrupt"))
	}))
	defer server.Close()
	client := NewClient(10*time.Second, Policy{})
	dest := filepath.Join(t.TempDir(), "app.bin")
	_, err := Download(context.Background(), client, DownloadOptions{
		URL:            server.URL,
		Dest:           dest,
		ExpectedSHA256: strings.Repeat("0", 64),
		Retries:        0,
	})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum error, got %v", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("unverified file became visible: %v", statErr)
	}
}

func TestDownloadRequiresHash(t *testing.T) {
	client := NewClient(time.Second, Policy{})
	_, err := Download(context.Background(), client, DownloadOptions{
		URL:  "http://127.0.0.1:1/a",
		Dest: filepath.Join(t.TempDir(), "a"),
	})
	if err == nil || !strings.Contains(err.Error(), "expected SHA-256") {
		t.Fatalf("expected hash refusal, got %v", err)
	}
}

func TestGetBytesRetriesAnd304(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("try later"))
			return
		}
		if r.Header.Get("If-None-Match") == `"etag-1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"etag-1"`)
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	}))
	defer server.Close()
	client := NewClient(10*time.Second, Policy{})
	body, headers, status, err := GetBytes(context.Background(), client, server.URL, nil, 1<<20, 2)
	if err != nil || status != http.StatusOK || string(body) != `{"ok":true}` {
		t.Fatalf("first GetBytes: %s %d %v", body, status, err)
	}
	if headers.Get("ETag") != `"etag-1"` {
		t.Fatalf("etag = %q", headers.Get("ETag"))
	}
	body, _, status, err = GetBytes(context.Background(), client, server.URL, map[string]string{"If-None-Match": `"etag-1"`}, 1<<20, 0)
	if err != nil || status != http.StatusNotModified || body != nil {
		t.Fatalf("304 GetBytes: %s %d %v", body, status, err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("2"); got != 2*time.Second {
		t.Fatalf("numeric Retry-After = %v", got)
	}
	if got := parseRetryAfter("0"); got != 0 {
		t.Fatalf("zero Retry-After = %v", got)
	}
}
