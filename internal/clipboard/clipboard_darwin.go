//go:build darwin

package clipboard

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

type darwinClipboard struct {
	logger          *slog.Logger
	lastHash        [32]byte
	lastChangeCount int64
	mu              sync.Mutex
}

// New creates a platform-specific Clipboard implementation.
func New(logger *slog.Logger) Clipboard {
	return &darwinClipboard{logger: logger}
}

func (c *darwinClipboard) Watch(ctx context.Context) (<-chan ClipData, error) {
	// TODO: Phase 1.3 — poll changeCount via cgo every poll interval
	ch := make(chan ClipData)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

func (c *darwinClipboard) SetText(text string) ([32]byte, error) {
	// TODO: Phase 1.3 — cgo writeClipboardText
	return [32]byte{}, errors.New("not implemented")
}

func (c *darwinClipboard) SetFileRefs(paths []string) ([32]byte, error) {
	// TODO: Phase 2.2 — NSFilenamesPboardType
	return [32]byte{}, errors.New("not implemented")
}

func (c *darwinClipboard) Hash() ([32]byte, error) {
	// TODO: Phase 1.3
	return [32]byte{}, errors.New("not implemented")
}
