package clipboard

import "context"

// Kind represents the type of clipboard content.
type Kind int

const (
	KindText  Kind = iota
	KindFiles
)

// ClipData holds clipboard content.
type ClipData struct {
	Kind  Kind
	Text  string     // KindText: content
	Files []FileMeta // KindFiles: file metadata
}

// FileMeta describes a file or directory in the clipboard.
type FileMeta struct {
	Name      string   // file name (no path)
	Size      int64    // size in bytes (0 for directories)
	SHA256    [32]byte // checksum
	LocalPath string   // full path on sender or staged path on receiver
	IsDir     bool     // true if this is a directory
}

// Clipboard provides platform-specific clipboard access.
type Clipboard interface {
	// Watch monitors the clipboard for changes. Sends to channel on each change.
	// Closes channel when ctx is cancelled.
	Watch(ctx context.Context) (<-chan ClipData, error)

	// SetText programmatically sets text in the clipboard. Returns hash for anti-loop.
	SetText(text string) ([32]byte, error)

	// SetFileRefs programmatically sets file references in the clipboard
	// (CF_HDROP on Windows / NSFilenamesPboardType on macOS).
	SetFileRefs(paths []string) ([32]byte, error)

	// Hash returns SHA-256 of current clipboard content.
	Hash() ([32]byte, error)
}
