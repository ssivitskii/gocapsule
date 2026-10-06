package capsule

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type limitWriter struct {
	w       *os.File
	written int64
	max     int64
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.written+int64(len(p)) > w.max {
		return 0, ErrBudgetExceeded
	}
	n, err := w.w.Write(p)
	w.written += int64(n)
	return n, err
}

func (r *Recorder) writeArchive(created time.Time, reason string, payloads []payload) (_ string, retErr error) {
	entries := make([]Entry, 0, len(payloads))
	var total int64
	for _, item := range payloads {
		if item.name == "manifest.json" || !validArchivePath(item.name) {
			return "", fmt.Errorf("invalid payload path %q", item.name)
		}
		size := int64(len(item.data))
		limit := r.cfg.Limits.PayloadBytes
		if item.name == "trace.out" {
			limit = r.cfg.Limits.TraceBytes
		}
		if size > limit {
			return "", fmt.Errorf("payload %s: %w", item.name, ErrBudgetExceeded)
		}
		total += size
		if total > r.cfg.Limits.TotalUncompressedBytes {
			return "", fmt.Errorf("payload total: %w", ErrBudgetExceeded)
		}
		sum := sha256.Sum256(item.data)
		entries = append(entries, Entry{Name: item.name, Size: size, SHA256: hex.EncodeToString(sum[:])})
	}
	manifest := Manifest{
		Schema: manifestSchema, Version: manifestVersion, CreatedAt: created,
		Reason: reason, GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Entries: entries,
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode manifest: %w", err)
	}
	manifestData = append(manifestData, '\n')
	if int64(len(manifestData))+total > r.cfg.Limits.TotalUncompressedBytes {
		return "", fmt.Errorf("archive total: %w", ErrBudgetExceeded)
	}

	if err := ensurePrivateDir(r.cfg.OutputDir); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(r.cfg.OutputDir, ".gocapsule-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary archive: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if retErr != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return "", fmt.Errorf("set archive permissions: %w", err)
	}

	lw := &limitWriter{w: tmp, max: r.cfg.Limits.ArchiveBytes}
	gz := gzip.NewWriter(lw)
	tw := tar.NewWriter(gz)
	write := func(name string, data []byte) error {
		header := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: created}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := write("manifest.json", manifestData); err != nil {
		return "", fmt.Errorf("write manifest: %w", err)
	}
	for _, item := range payloads {
		if err := write(item.name, item.data); err != nil {
			return "", fmt.Errorf("write payload %s: %w", item.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return "", fmt.Errorf("finalize tar archive: %w", err)
	}
	if err := gz.Close(); err != nil {
		return "", fmt.Errorf("finalize compressed archive: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("sync archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close archive: %w", err)
	}

	suffix := strings.TrimSuffix(filepath.Base(tmpName), ".tmp")
	suffix = strings.TrimPrefix(suffix, ".gocapsule-")
	finalName := fmt.Sprintf("gocapsule-%s-%s-%s.tar.gz", created.Format("20060102T150405.000000000Z"), reason, suffix)
	finalPath := filepath.Join(r.cfg.OutputDir, finalName)
	if err := os.Rename(tmpName, finalPath); err != nil {
		return "", fmt.Errorf("publish archive atomically: %w", err)
	}
	return finalPath, nil
}

func ensurePrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("output path is not a directory: %s", dir)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("inspect output directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	info, err = os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("output path did not resolve to a newly created directory")
	}
	return nil
}

func validArchivePath(name string) bool {
	if name == "" || path.IsAbs(name) || strings.Contains(name, "\\") || looksLikeWindowsAbsolutePath(name) {
		return false
	}
	clean := path.Clean(name)
	return clean == name && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func looksLikeWindowsAbsolutePath(name string) bool {
	return len(name) >= 3 && ((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z')) && name[1] == ':' && name[2] == '/'
}
