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
// On name collision: appends suffix "_1", "_2", etc.
// After writing, verifies SHA-256 checksum. On mismatch, deletes the file and returns error.
// Returns the full path of the staged file.
func (m *Manager) Stage(name string, size int64, checksum [32]byte, r io.Reader) (string, error) {
	// Idempotent recreate if directory was deleted while running
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return "", fmt.Errorf("ensure staging dir: %w", err)
	}

	path := m.uniquePath(name)

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

// uniquePath returns a non-colliding path in the staging directory.
func (m *Manager) uniquePath(name string) string {
	path := filepath.Join(m.dir, name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}

	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		candidate := filepath.Join(m.dir, fmt.Sprintf("%s_%d%s", base, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// Path returns the full path of the staging directory.
func (m *Manager) Path() string {
	return m.dir
}

// Clean removes files older than maxAge. Returns number of files removed.
func (m *Manager) Clean(maxAge time.Duration) (int, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(m.dir, e.Name())); err == nil {
				removed++
			}
		}
	}
	if removed > 0 {
		m.logger.Info("staged files cleaned", "removed", removed)
	}
	return removed, nil
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
