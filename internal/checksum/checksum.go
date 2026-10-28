// Package checksum parses and computes SHA-256 digests.
package checksum

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// File computes the SHA-256 and size of path.
func File(path string) (digest string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Parse parses a checksums file in GNU sha256sum format. Both text ("  name")
// and binary (" *name") mode markers are accepted. Names are indexed exactly
// and by base name for compatibility with relative paths.
func Parse(data []byte) (map[string]string, error) {
	out := make(map[string]string)
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("line %d: expected '<sha256>  <filename>'", lineNo)
		}
		digest := strings.ToLower(fields[0])
		if len(digest) != sha256.Size*2 {
			return nil, fmt.Errorf("line %d: invalid SHA-256 length", lineNo)
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return nil, fmt.Errorf("line %d: invalid SHA-256", lineNo)
		}
		name := strings.TrimPrefix(strings.Join(fields[1:], " "), "*")
		name = strings.TrimPrefix(name, "./")
		if name == "" {
			return nil, fmt.Errorf("line %d: empty filename", lineNo)
		}
		out[name] = digest
		out[path.Base(name)] = digest
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Lookup finds a digest by exact name or base name.
func Lookup(sums map[string]string, name string) (string, bool) {
	if v, ok := sums[name]; ok {
		return v, true
	}
	v, ok := sums[path.Base(name)]
	return v, ok
}
