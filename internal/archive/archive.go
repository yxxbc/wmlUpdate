// Package archive safely extracts and creates ZIP and tar archives. Extraction
// treats archives as untrusted input: absolute paths, traversal, escaping
// symlinks, hardlinks, special files and decompression bombs are rejected.
package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	defaultMaxBytes = int64(4 << 30)
	defaultMaxFiles = 200000
	symlinkLimit    = 8 << 10
)

// ExtractOptions bounds extraction and controls symlink support.
type ExtractOptions struct {
	StripComponents int
	MaxBytes        int64
	MaxFiles        int
	AllowSymlinks   bool
}

// DetectFormat returns zip, tar.gz or tar based on magic bytes, then filename.
func DetectFormat(path string) string {
	f, err := os.Open(path)
	if err == nil {
		head := make([]byte, 512)
		n, _ := io.ReadFull(f, head)
		_ = f.Close()
		head = head[:n]
		if len(head) >= 4 && head[0] == 'P' && head[1] == 'K' && (head[2] == 3 || head[2] == 5) {
			return "zip"
		}
		if len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b {
			return "tar.gz"
		}
		if len(head) >= 262 && string(head[257:262]) == "ustar" {
			return "tar"
		}
	}
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return "zip"
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return "tar.gz"
	case strings.HasSuffix(lower, ".tar"):
		return "tar"
	default:
		return ""
	}
}

// Extract extracts archivePath into dest. dest is created if necessary and
// must be empty or absent.
func Extract(archivePath, dest, format string, opts ExtractOptions) error {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = defaultMaxBytes
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = defaultMaxFiles
	}
	if opts.StripComponents < 0 {
		return errors.New("strip_components cannot be negative")
	}
	if format == "" {
		format = DetectFormat(archivePath)
	}
	switch format {
	case "zip":
	case "tar.gz":
	case "tar":
	default:
		return fmt.Errorf("unsupported archive format %q", format)
	}
	if err := ensureEmptyDir(dest); err != nil {
		return err
	}
	state := &extractState{dest: dest, opts: opts, dirModes: make(map[string]os.FileMode)}
	var err error
	switch format {
	case "zip":
		err = extractZip(archivePath, state)
	case "tar.gz":
		f, openErr := os.Open(archivePath)
		if openErr != nil {
			return openErr
		}
		defer f.Close()
		gz, gzErr := gzip.NewReader(f)
		if gzErr != nil {
			return fmt.Errorf("open gzip stream: %w", gzErr)
		}
		defer gz.Close()
		err = extractTar(gz, state)
	case "tar":
		f, openErr := os.Open(archivePath)
		if openErr != nil {
			return openErr
		}
		defer f.Close()
		err = extractTar(f, state)
	}
	if err != nil {
		return err
	}
	// Apply directory modes after all children have been written; a read-only
	// directory entry earlier in the archive must not block extraction.
	for dir, mode := range state.dirModes {
		_ = os.Chmod(dir, mode)
	}
	return nil
}

func ensureEmptyDir(dest string) error {
	info, err := os.Stat(dest)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("extraction destination %s is not a directory", dest)
		}
		entries, err := os.ReadDir(dest)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return fmt.Errorf("extraction destination %s is not empty", dest)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.MkdirAll(dest, 0o700)
}

type extractState struct {
	dest     string
	opts     ExtractOptions
	files    int
	bytes    int64
	dirModes map[string]os.FileMode
}

func (s *extractState) entryPath(raw string) (string, string, error) {
	rel, err := cleanArchivePath(raw, s.opts.StripComponents)
	if err != nil {
		return "", "", err
	}
	if rel == "" {
		return "", "", nil
	}
	target := filepath.Join(s.dest, filepath.FromSlash(rel))
	if !within(s.dest, target) {
		return "", "", fmt.Errorf("archive entry %q escapes destination", raw)
	}
	return rel, target, nil
}

func cleanArchivePath(raw string, strip int) (string, error) {
	if strings.ContainsRune(raw, 0) {
		return "", errors.New("archive entry contains NUL byte")
	}
	raw = strings.ReplaceAll(raw, `\`, "/")
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("archive entry %q is absolute", raw)
	}
	// Reject Windows drive-letter and UNC paths before path.Clean can hide them.
	if len(raw) >= 2 && raw[1] == ':' || strings.HasPrefix(raw, "//") {
		return "", fmt.Errorf("archive entry %q has a volume path", raw)
	}
	clean := path.Clean(raw)
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("archive entry %q escapes destination", raw)
	}
	for i := 0; i < strip; i++ {
		if clean == "" || clean == "." {
			return "", nil
		}
		if idx := strings.IndexByte(clean, '/'); idx >= 0 {
			clean = clean[idx+1:]
		} else {
			return "", nil
		}
	}
	clean = strings.TrimPrefix(clean, "./")
	if clean == "" || clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("archive entry %q escapes destination after strip", raw)
	}
	return clean, nil
}

func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// mkdirParents creates parents without following symlinks. Rejecting symlinked
// parents closes the classic "symlink then write through it" archive escape.
func (s *extractState) mkdirParents(target string) error {
	parent := filepath.Dir(target)
	if !within(s.dest, parent) {
		return errors.New("parent escapes destination")
	}
	rel, err := filepath.Rel(s.dest, parent)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	cur := s.dest
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(cur, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("create directory: %w", err)
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive parent %s is a symlink", cur)
		}
		if !info.IsDir() {
			return fmt.Errorf("archive parent %s is not a directory", cur)
		}
	}
	return nil
}

func (s *extractState) countEntry() error {
	s.files++
	if s.files > s.opts.MaxFiles {
		return fmt.Errorf("archive exceeds %d entries", s.opts.MaxFiles)
	}
	return nil
}

func (s *extractState) addFile(target string, mode os.FileMode, size int64, r io.Reader) error {
	if err := s.countEntry(); err != nil {
		return err
	}
	if size > 0 && s.bytes+size > s.opts.MaxBytes {
		return fmt.Errorf("archive expands beyond %d bytes", s.opts.MaxBytes)
	}
	if err := s.mkdirParents(target); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace symlink %s with a file", target)
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	remaining := s.opts.MaxBytes - s.bytes
	written, copyErr := io.Copy(f, io.LimitReader(r, remaining+1))
	if copyErr == nil && written > remaining {
		copyErr = fmt.Errorf("archive expands beyond %d bytes", s.opts.MaxBytes)
	}
	if closeErr := f.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(target)
		return copyErr
	}
	s.bytes += written
	if mode == 0 {
		mode = 0o644
	}
	if err := os.Chmod(target, mode.Perm()&0o755); err != nil && !errors.Is(err, os.ErrPermission) {
		return err
	}
	return nil
}

func (s *extractState) addDir(raw string) error {
	if err := s.countEntry(); err != nil {
		return err
	}
	rel, target, err := s.entryPath(raw)
	if err != nil {
		return err
	}
	if rel == "" {
		return nil
	}
	if err := s.mkdirParents(target); err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	s.dirModes[target] = 0o755
	return nil
}

func (s *extractState) addSymlink(raw, linkTarget string, mode os.FileMode) error {
	if err := s.countEntry(); err != nil {
		return err
	}
	if !s.opts.AllowSymlinks {
		return fmt.Errorf("archive contains symlink %q but symlinks are disabled", raw)
	}
	rel, target, err := s.entryPath(raw)
	if err != nil {
		return err
	}
	if rel == "" {
		return nil
	}
	linkTarget = strings.ReplaceAll(linkTarget, `\`, "/")
	if linkTarget == "" || strings.HasPrefix(linkTarget, "/") || (len(linkTarget) >= 2 && linkTarget[1] == ':') {
		return fmt.Errorf("symlink %q has an absolute target", raw)
	}
	resolved := path.Clean(path.Join(path.Dir(rel), linkTarget))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("symlink %q points outside the archive", raw)
	}
	if err := s.mkdirParents(target); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil {
		if info.IsDir() {
			return fmt.Errorf("symlink %q collides with a directory", raw)
		}
		_ = os.Remove(target)
	}
	if err := os.Symlink(filepath.FromSlash(linkTarget), target); err != nil {
		return fmt.Errorf("create symlink %q: %w", raw, err)
	}
	return nil
}

func extractZip(archivePath string, s *extractState) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			data, err := io.ReadAll(io.LimitReader(rc, symlinkLimit+1))
			_ = rc.Close()
			if err != nil {
				return err
			}
			if len(data) > symlinkLimit {
				return fmt.Errorf("symlink %q target is too long", f.Name)
			}
			if err := s.addSymlink(f.Name, string(data), mode); err != nil {
				return err
			}
			continue
		}
		if f.FileInfo().IsDir() || strings.HasSuffix(f.Name, "/") {
			if err := s.addDir(f.Name); err != nil {
				return err
			}
			continue
		}
		if mode&os.ModeType != 0 {
			return fmt.Errorf("archive entry %q has unsupported file type %s", f.Name, mode.Type())
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %q: %w", f.Name, err)
		}
		size := int64(f.UncompressedSize64)
		if f.UncompressedSize64 > uint64(s.opts.MaxBytes) {
			_ = rc.Close()
			return fmt.Errorf("archive entry %q is too large", f.Name)
		}
		err = s.addFileFromEntry(f.Name, mode, size, rc)
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *extractState) addFileFromEntry(raw string, mode os.FileMode, size int64, r io.Reader) error {
	rel, target, err := s.entryPath(raw)
	if err != nil {
		return err
	}
	if rel == "" {
		return nil
	}
	return s.addFile(target, mode, size, r)
}

func extractTar(r io.Reader, s *extractState) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := s.addDir(hdr.Name); err != nil {
				return err
			}
		case tar.TypeReg, 0:
			mode := os.FileMode(hdr.Mode)
			if err := s.addFileFromEntry(hdr.Name, mode, hdr.Size, tr); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := s.addSymlink(hdr.Name, hdr.Linkname, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeLink:
			return fmt.Errorf("archive entry %q is a hardlink; hardlinks are not supported", hdr.Name)
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		default:
			return fmt.Errorf("archive entry %q has unsupported type %q", hdr.Name, hdr.Typeflag)
		}
	}
}

// Create archives the contents of dir into dest using zip, tar.gz or tar.
// Symlinks are preserved. The archive is written atomically.
func Create(dir, dest, format string) error {
	switch format {
	case "zip", "tar.gz", "tar":
	default:
		return fmt.Errorf("unsupported archive format %q", format)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat archive source: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("archive source %s is not a directory", dir)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(absDir, absDest); err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
		return fmt.Errorf("archive destination must not be inside the source directory")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".archive-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := createTo(tmp, dir, format); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

func createTo(f *os.File, dir, format string) error {
	switch format {
	case "zip":
		zw := zip.NewWriter(f)
		if err := writeZipDir(zw, dir); err != nil {
			_ = zw.Close()
			return err
		}
		return zw.Close()
	case "tar.gz":
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		if err := writeTarDir(tw, dir); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return err
		}
		if err := tw.Close(); err != nil {
			_ = gz.Close()
			return err
		}
		return gz.Close()
	case "tar":
		tw := tar.NewWriter(f)
		if err := writeTarDir(tw, dir); err != nil {
			_ = tw.Close()
			return err
		}
		return tw.Close()
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func writeZipDir(zw *zip.Writer, root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = rel
		if info.IsDir() {
			hdr.Name += "/"
			hdr.Method = zip.Store
		} else {
			hdr.Method = zip.Deflate
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			_, err = io.WriteString(w, target)
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported file type at %s", p)
		}
		file, err := os.Open(p)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(w, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func writeTarDir(tw *tar.Writer, root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		hdr.Uid = 0
		hdr.Gid = 0
		hdr.Uname = ""
		hdr.Gname = ""
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			hdr.Linkname = target
			hdr.Typeflag = tar.TypeSymlink
			hdr.Size = 0
		}
		if !info.IsDir() && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("unsupported file type at %s", p)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		file, err := os.Open(p)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}
