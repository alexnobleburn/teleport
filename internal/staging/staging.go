package staging

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const stagingDir = ".teleport/staged"

// Manager handles the staging directory for received files.
type Manager struct {
	dir    string
	logger *slog.Logger
}

// New creates a staging manager. Creates the staging directory if it doesn't exist.
func New(logger *slog.Logger) (*Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, stagingDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Manager{dir: dir, logger: logger}, nil
}

// Stage writes a file to the staging directory. Streams from reader without loading into RAM.
// name may contain forward slashes for directory files (e.g., "MyFolder/sub/file.txt") —
// intermediate directories are created automatically.
// On name collision: appends suffix "_1", "_2", etc. to the leaf filename.
// After writing, verifies SHA-256 checksum. On mismatch, deletes the file and returns error.
// Returns the full path of the staged file.
func (m *Manager) Stage(name string, size int64, checksum [32]byte, r io.Reader) (string, error) {
	// Validate name to prevent path traversal (name comes from network)
	if err := validateName(name); err != nil {
		return "", err
	}

	// Idempotent recreate if directory was deleted while running
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return "", fmt.Errorf("ensure staging dir: %w", err)
	}

	path := m.uniquePath(name)

	// Ensure parent directories exist (for names with subdirectories)
	if dir := filepath.Dir(path); dir != m.dir {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("create staged subdirs: %w", err)
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("create staged file: %w", err)
	}

	h := sha256.New()
	written, err := io.Copy(f, io.TeeReader(r, h))
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return "", fmt.Errorf("write staged file: %w", err)
	}

	// Verify checksum
	var actual [32]byte
	copy(actual[:], h.Sum(nil))
	if actual != checksum {
		os.Remove(path)
		return "", fmt.Errorf("checksum mismatch for %q: expected %x, got %x", name, checksum[:8], actual[:8])
	}

	m.logger.Info("file staged", "name", name, "size", humanSize(written), "path", path)
	return path, nil
}

// validateName rejects file names that could escape the staging directory.
func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("empty file name")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("invalid file name (contains ..): %q", name)
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") {
		return fmt.Errorf("invalid file name (absolute path): %q", name)
	}
	return nil
}

// uniquePath returns a non-colliding path in the staging directory.
// name may contain forward slashes for subdirectory files (e.g., "MyFolder/sub/file.txt").
// Uniqueness suffix is applied only to the leaf filename.
func (m *Manager) uniquePath(name string) string {
	// Normalize forward slashes to OS path separator
	osName := filepath.FromSlash(name)
	path := filepath.Join(m.dir, osName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}

	dir := filepath.Dir(path)
	base := filepath.Base(osName)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 1; ; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s_%d%s", stem, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// Path returns the full path of the staging directory.
func (m *Manager) Path() string {
	return m.dir
}

// Clean removes files and directories older than maxAge. Returns number of entries removed.
// For directories, the newest file inside determines the directory's age.
func (m *Manager) Clean(maxAge time.Duration) (int, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, e := range entries {
		path := filepath.Join(m.dir, e.Name())
		if e.IsDir() {
			if m.isDirOlderThan(path, cutoff) {
				if err := os.RemoveAll(path); err == nil {
					removed++
				}
			}
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err == nil {
				removed++
			}
		}
	}
	if removed > 0 {
		m.logger.Info("staged files cleaned", "removed", removed)
	}
	return removed, nil
}

// isDirOlderThan returns true if all files in dir (recursively) are older than cutoff.
func (m *Manager) isDirOlderThan(dir string, cutoff time.Time) bool {
	old := true
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if !info.ModTime().Before(cutoff) {
			old = false
			return filepath.SkipAll
		}
		return nil
	})
	return old
}

// StartCleaner runs a goroutine that periodically removes old staged files.
func (m *Manager) StartCleaner(ctx context.Context, maxAge, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := m.Clean(maxAge); err != nil {
					m.logger.Debug("staging clean error", "error", err)
				}
			}
		}
	}()
}

func humanSize(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(bytes)/(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
