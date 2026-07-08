package staging

import (
	"bytes"
	"crypto/sha256"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	return &Manager{dir: dir, logger: slog.Default()}
}

func TestStage_Basic(t *testing.T) {
	m := testManager(t)
	content := []byte("hello world")
	checksum := sha256.Sum256(content)

	path, err := m.Stage("test.txt", int64(len(content)), checksum, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch: got %q", got)
	}
	if filepath.Base(path) != "test.txt" {
		t.Fatalf("unexpected filename: %s", filepath.Base(path))
	}
}

func TestStage_Collision(t *testing.T) {
	m := testManager(t)
	content1 := []byte("file1")
	content2 := []byte("file2")
	cs1 := sha256.Sum256(content1)
	cs2 := sha256.Sum256(content2)

	p1, err := m.Stage("report.pdf", int64(len(content1)), cs1, bytes.NewReader(content1))
	if err != nil {
		t.Fatal(err)
	}
	p2, err := m.Stage("report.pdf", int64(len(content2)), cs2, bytes.NewReader(content2))
	if err != nil {
		t.Fatal(err)
	}

	if p1 == p2 {
		t.Fatal("paths should differ")
	}
	if filepath.Base(p1) != "report.pdf" {
		t.Fatalf("first file: %s", filepath.Base(p1))
	}
	if filepath.Base(p2) != "report_1.pdf" {
		t.Fatalf("second file: %s", filepath.Base(p2))
	}
}

func TestStage_ChecksumMismatch(t *testing.T) {
	m := testManager(t)
	content := []byte("data")
	badChecksum := sha256.Sum256([]byte("wrong"))

	_, err := m.Stage("bad.txt", int64(len(content)), badChecksum, bytes.NewReader(content))
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}

	// File should be deleted
	entries, _ := os.ReadDir(m.dir)
	if len(entries) != 0 {
		t.Fatalf("file should be deleted on checksum mismatch, found %d files", len(entries))
	}
}

func TestStage_LargeFile(t *testing.T) {
	m := testManager(t)
	size := 5 * 1024 * 1024 // 5MB (fast enough for tests)
	content := bytes.Repeat([]byte("A"), size)
	checksum := sha256.Sum256(content)

	path, err := m.Stage("large.bin", int64(size), checksum, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(size) {
		t.Fatalf("size: got %d, want %d", info.Size(), size)
	}
}

func TestClean_OldFiles(t *testing.T) {
	m := testManager(t)

	// Create an "old" file
	oldPath := filepath.Join(m.dir, "old.txt")
	os.WriteFile(oldPath, []byte("old"), 0o644)
	os.Chtimes(oldPath, time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour))

	// Create a "recent" file
	recentPath := filepath.Join(m.dir, "recent.txt")
	os.WriteFile(recentPath, []byte("recent"), 0o644)

	removed, err := m.Clean(1 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 removed, got %d", removed)
	}

	// Old should be gone, recent should remain
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatal("old file should be deleted")
	}
	if _, err := os.Stat(recentPath); err != nil {
		t.Fatal("recent file should remain")
	}
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}
	for _, tt := range tests {
		got := humanSize(tt.bytes)
		if got != tt.want {
			t.Errorf("humanSize(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}
