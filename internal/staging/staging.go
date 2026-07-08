package staging

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

const stagingDir = ".teleport/staged"

// Manager handles the staging directory for received files.
type Manager struct {
	dir    string // full path: os.UserHomeDir() + stagingDir
	logger *slog.Logger
}

// New creates a staging manager. Creates the staging directory if it doesn't exist.
func New(logger *slog.Logger) (*Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, stagingDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Manager{dir: dir, logger: logger}, nil
}

// Path returns the full path of the staging directory.
func (m *Manager) Path() string {
	return m.dir
}

// Clean removes files older than maxAge. Returns number of files removed.
func (m *Manager) Clean(maxAge time.Duration) (int, error) {
	// TODO: Phase 3.3
	return 0, nil
}
