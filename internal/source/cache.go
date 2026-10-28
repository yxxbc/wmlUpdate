package source

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type cachedResponse struct {
	URL          string `json:"url"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
	Body         []byte `json:"body"`
}

type metadataCache struct {
	dir string
	mu  sync.Mutex
}

func newMetadataCache(dir string) *metadataCache {
	return &metadataCache{dir: dir}
}

func (c *metadataCache) path(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return filepath.Join(c.dir, "http-"+hex.EncodeToString(sum[:8])+".json")
}

func (c *metadataCache) get(rawURL string) (cachedResponse, bool) {
	if c == nil || c.dir == "" {
		return cachedResponse{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := os.ReadFile(c.path(rawURL))
	if err != nil {
		return cachedResponse{}, false
	}
	var entry cachedResponse
	if err := json.Unmarshal(data, &entry); err != nil || entry.URL != rawURL || len(entry.Body) == 0 {
		return cachedResponse{}, false
	}
	return entry, true
}

func (c *metadataCache) put(entry cachedResponse) error {
	if c == nil || c.dir == "" || (entry.ETag == "" && entry.LastModified == "") {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	path := c.path(entry.URL)
	tmp, err := os.CreateTemp(c.dir, ".http-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
