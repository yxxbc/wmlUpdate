// Package httpx provides a hardened HTTP client, bounded retries and resumable
// downloads for the updater.
package httpx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Policy constrains outbound requests.
type Policy struct {
	UserAgent     string
	AllowInsecure bool
	AllowedHosts  []string
	MaxRedirects  int
}

// NewClient builds a client with scheme, host and redirect enforcement.
func NewClient(timeout time.Duration, policy Policy) *http.Client {
	if policy.MaxRedirects <= 0 {
		policy.MaxRedirects = 10
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 16
	transport.MaxIdleConnsPerHost = 4
	transport.IdleConnTimeout = 90 * time.Second
	transport.TLSHandshakeTimeout = 15 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{
		Timeout:   timeout,
		Transport: &policyTransport{base: transport, policy: policy},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= policy.MaxRedirects {
				return fmt.Errorf("stopped after %d redirects", policy.MaxRedirects)
			}
			return nil
		},
	}
}

type policyTransport struct {
	base   http.RoundTripper
	policy Policy
}

func (t *policyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := CheckURL(req.URL, t.policy); err != nil {
		return nil, err
	}
	if t.policy.UserAgent != "" && req.Header.Get("User-Agent") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", t.policy.UserAgent)
	}
	return t.base.RoundTrip(req)
}

// CheckURL enforces allowed schemes and hosts.
func CheckURL(u *url.URL, policy Policy) error {
	if u == nil {
		return errors.New("empty URL")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !policy.AllowInsecure && !isLoopbackHost(u.Hostname()) {
			return fmt.Errorf("refusing insecure HTTP URL %q (set security.allow_insecure_http for a trusted network)", redactURL(u))
		}
	default:
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if len(policy.AllowedHosts) > 0 && !hostAllowed(u, policy.AllowedHosts) {
		return fmt.Errorf("host %q is not in security.allowed_hosts", u.Host)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func hostAllowed(u *url.URL, allowed []string) bool {
	host := strings.ToLower(u.Host)
	name := strings.ToLower(u.Hostname())
	for _, entry := range allowed {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if entry == host || entry == name {
			return true
		}
		if strings.HasPrefix(entry, "*.") {
			suffix := entry[1:] // ".example.com"
			if strings.HasSuffix(name, suffix) && name != strings.TrimPrefix(suffix, ".") {
				return true
			}
		}
	}
	return false
}

func redactURL(u *url.URL) string {
	copy := *u
	copy.User = nil
	copy.RawQuery = ""
	copy.Fragment = ""
	return copy.String()
}

// StatusError is returned for non-successful HTTP responses.
type StatusError struct {
	StatusCode int
	Status     string
	RetryAfter time.Duration
	Body       string
	URL        string
}

func (e *StatusError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("HTTP %s from %s: %s", e.Status, e.URL, e.Body)
	}
	return fmt.Sprintf("HTTP %s from %s", e.Status, e.URL)
}

// GetBytes performs a retrying GET and returns the body, response headers and
// status. A 304 is returned to the caller with nil body.
func GetBytes(ctx context.Context, client *http.Client, rawURL string, headers map[string]string, maxBytes int64, retries int) ([]byte, http.Header, int, error) {
	if maxBytes <= 0 {
		maxBytes = 8 << 20
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			retryAfter := time.Duration(0)
			var se *StatusError
			if errors.As(lastErr, &se) {
				retryAfter = se.RetryAfter
			}
			if err := sleepBackoff(ctx, attempt, retryAfter); err != nil {
				return nil, nil, 0, err
			}
		}
		u, err := url.Parse(rawURL)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("parse URL: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("create request: %w", err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, 0, ctx.Err()
			}
			lastErr = err
			if !retryableNetworkError(err) {
				return nil, nil, 0, err
			}
			continue
		}

		var body []byte
		var readErr error
		if resp.StatusCode != http.StatusNotModified {
			body, readErr = readLimited(resp.Body, maxBytes)
		}
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusNotModified {
			return nil, resp.Header, resp.StatusCode, nil
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if readErr != nil {
				lastErr = readErr
				return nil, resp.Header, resp.StatusCode, readErr
			}
			return body, resp.Header, resp.StatusCode, nil
		}
		se := &StatusError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			Body:       strings.TrimSpace(string(body)),
			URL:        redactURL(resp.Request.URL),
		}
		if len(se.Body) > 4096 {
			se.Body = se.Body[:4096]
		}
		lastErr = se
		if !retryableStatus(se) {
			return nil, resp.Header, resp.StatusCode, se
		}
	}
	return nil, nil, 0, lastErr
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	var b strings.Builder
	n, err := io.Copy(&b, io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return []byte(b.String()), nil
}

func retryableStatus(err error) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	switch se.StatusCode {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func retryableNetworkError(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

func sleepBackoff(ctx context.Context, attempt int, retryAfter time.Duration) error {
	if retryAfter <= 0 {
		base := 300 * time.Millisecond
		maxDelay := 8 * time.Second
		d := base << min(attempt-1, 5)
		if d > maxDelay {
			d = maxDelay
		}
		jitter := time.Duration(rand.Int64N(int64(d/2 + 1)))
		retryAfter = d/2 + jitter
	}
	timer := time.NewTimer(retryAfter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseRetryAfter(v string) time.Duration {
	const cap = 60 * time.Second
	var d time.Duration
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
		d = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = time.Until(t)
	}
	if d > cap {
		d = cap
	}
	if d < 0 {
		return 0
	}
	return d
}

// DownloadOptions configures Download.
type DownloadOptions struct {
	URL            string
	Headers        map[string]string
	Dest           string
	ExpectedSHA256 string
	ExpectedSize   int64
	MaxBytes       int64
	Retries        int
	Progress       func(current, total int64)
	// AllowUnverified permits a download without a digest. This exists only
	// for the explicitly opt-in security.allow_unverified_assets mode.
	AllowUnverified bool
}

// ErrChecksum identifies an artifact that does not match its expected digest.
var ErrChecksum = errors.New("checksum mismatch")

// Download fetches URL to Dest, resuming a .part file when possible. The final
// file is only visible after size and SHA-256 verification succeed.
func Download(ctx context.Context, client *http.Client, opts DownloadOptions) (int64, error) {
	if opts.Dest == "" {
		return 0, errors.New("download destination is empty")
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 2 << 30
	}
	if opts.ExpectedSize > opts.MaxBytes {
		return 0, fmt.Errorf("artifact size %d exceeds max_bytes %d", opts.ExpectedSize, opts.MaxBytes)
	}
	if opts.ExpectedSHA256 != "" {
		opts.ExpectedSHA256 = strings.ToLower(strings.TrimSpace(opts.ExpectedSHA256))
		if len(opts.ExpectedSHA256) != sha256.Size*2 {
			return 0, errors.New("expected SHA-256 must be 64 hex characters")
		}
		if _, err := hex.DecodeString(opts.ExpectedSHA256); err != nil {
			return 0, errors.New("expected SHA-256 is not hexadecimal")
		}
	} else if !opts.AllowUnverified {
		return 0, errors.New("refusing download without an expected SHA-256")
	}
	if err := os.MkdirAll(filepath.Dir(opts.Dest), 0o700); err != nil {
		return 0, fmt.Errorf("create download directory: %w", err)
	}
	part := opts.Dest + ".part"

	if opts.ExpectedSize > 0 {
		if info, err := os.Stat(part); err == nil && info.Size() == opts.ExpectedSize {
			if err := verifyFile(part, opts.ExpectedSHA256, opts.ExpectedSize, opts.AllowUnverified); err == nil {
				if err := os.Rename(part, opts.Dest); err == nil {
					return info.Size(), nil
				}
			}
			_ = os.Remove(part)
		}
	}

	var lastErr error
	var downloaded int64
	for attempt := 0; attempt <= opts.Retries; attempt++ {
		if attempt > 0 {
			retryAfter := time.Duration(0)
			var se *StatusError
			if errors.As(lastErr, &se) {
				retryAfter = se.RetryAfter
			}
			if err := sleepBackoff(ctx, attempt, retryAfter); err != nil {
				return downloaded, err
			}
		}
		n, err := downloadOnce(ctx, client, opts, part)
		downloaded = n
		if err == nil {
			if err = verifyFile(part, opts.ExpectedSHA256, opts.ExpectedSize, opts.AllowUnverified); err == nil {
				lastErr = nil
				break
			}
		}
		lastErr = err
		if ctx.Err() != nil {
			return downloaded, ctx.Err()
		}
		if errors.Is(err, ErrChecksum) || errors.Is(err, io.ErrUnexpectedEOF) {
			_ = os.Remove(part)
			continue
		}
		var se *StatusError
		if errors.As(err, &se) && se.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			// downloadOnce removed the stale partial; retry from byte zero.
			continue
		}
		if !retryableStatus(err) && !retryableNetworkError(err) {
			return downloaded, err
		}
	}
	if lastErr != nil {
		return downloaded, lastErr
	}
	if err := os.Rename(part, opts.Dest); err != nil {
		return downloaded, fmt.Errorf("finalize download: %w", err)
	}
	if opts.Progress != nil && opts.ExpectedSize <= 0 {
		opts.Progress(downloaded, downloaded)
	}
	return downloaded, nil
}

func downloadOnce(ctx context.Context, client *http.Client, opts DownloadOptions, part string) (int64, error) {
	var offset int64
	if info, err := os.Stat(part); err == nil {
		offset = info.Size()
		if offset > opts.MaxBytes {
			_ = os.Remove(part)
			offset = 0
		}
	}
	u, err := url.Parse(opts.URL)
	if err != nil {
		return offset, fmt.Errorf("parse download URL: %w", err)
	}
	headers := make(map[string]string, len(opts.Headers)+3)
	for k, v := range opts.Headers {
		headers[k] = v
	}
	headers["Accept-Encoding"] = "identity"
	if offset > 0 {
		headers["Range"] = fmt.Sprintf("bytes=%d-", offset)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return offset, fmt.Errorf("create request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return offset, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		if offset > 0 {
			offset = 0 // server ignored Range
		}
	case http.StatusPartialContent:
		if offset == 0 {
			return 0, errors.New("server returned partial content without a range request")
		}
		cr := resp.Header.Get("Content-Range")
		if !strings.HasPrefix(cr, fmt.Sprintf("bytes %d-", offset)) {
			return offset, fmt.Errorf("unexpected Content-Range %q", cr)
		}
	case http.StatusRequestedRangeNotSatisfiable:
		_ = os.Remove(part)
		return 0, &StatusError{StatusCode: resp.StatusCode, Status: resp.Status, URL: redactURL(u), RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		body, _ := readLimited(resp.Body, 8<<10)
		return offset, &StatusError{StatusCode: resp.StatusCode, Status: resp.Status, URL: redactURL(u), Body: strings.TrimSpace(string(body)), RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	default:
		body, _ := readLimited(resp.Body, 8<<10)
		return offset, &StatusError{StatusCode: resp.StatusCode, Status: resp.Status, URL: redactURL(u), Body: strings.TrimSpace(string(body))}
	}

	total := opts.ExpectedSize
	if total == 0 && resp.ContentLength >= 0 {
		total = offset + resp.ContentLength
	}
	if total > opts.MaxBytes {
		return offset, fmt.Errorf("artifact size %d exceeds max_bytes %d", total, opts.MaxBytes)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return offset, fmt.Errorf("open partial download: %w", err)
	}

	buf := make([]byte, 256<<10)
	written := offset
	lastProgress := time.Now()
	var copyErr error
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			written += int64(n)
			if written > opts.MaxBytes {
				copyErr = fmt.Errorf("download exceeds max_bytes %d", opts.MaxBytes)
				break
			}
			if _, err := f.Write(buf[:n]); err != nil {
				copyErr = fmt.Errorf("write download: %w", err)
				break
			}
			if opts.Progress != nil && (time.Since(lastProgress) > 100*time.Millisecond || written == total) {
				opts.Progress(written, total)
				lastProgress = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			copyErr = fmt.Errorf("read download: %w", readErr)
			break
		}
	}
	if err := f.Sync(); err != nil && copyErr == nil {
		copyErr = fmt.Errorf("sync download: %w", err)
	}
	if err := f.Close(); err != nil && copyErr == nil {
		copyErr = fmt.Errorf("close download: %w", err)
	}
	if copyErr != nil {
		return written, copyErr
	}
	if opts.ExpectedSize > 0 && written != opts.ExpectedSize {
		return written, fmt.Errorf("downloaded %d bytes, expected %d: %w", written, opts.ExpectedSize, io.ErrUnexpectedEOF)
	}
	return written, nil
}

func verifyFile(path, expectedSHA256 string, expectedSize int64, allowUnverified bool) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat download: %w", err)
	}
	if expectedSize > 0 && info.Size() != expectedSize {
		return fmt.Errorf("size mismatch: got %d, want %d", info.Size(), expectedSize)
	}
	if expectedSHA256 == "" {
		if allowUnverified {
			return nil
		}
		return errors.New("no expected SHA-256 available; refusing unverified artifact")
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open download for verification: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash download: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, expectedSHA256) {
		return fmt.Errorf("%w: got %s, want %s", ErrChecksum, got, expectedSHA256)
	}
	return nil
}
