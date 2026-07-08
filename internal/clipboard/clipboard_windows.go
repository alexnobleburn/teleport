//go:build windows

package clipboard

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

type windowsClipboard struct {
	logger   *slog.Logger
	lastHash [32]byte
	mu       sync.Mutex
}

// New creates a platform-specific Clipboard implementation.
func New(logger *slog.Logger) Clipboard {
	return &windowsClipboard{logger: logger}
}

func (c *windowsClipboard) Watch(ctx context.Context) (<-chan ClipData, error) {
	// TODO: Phase 1.3 — AddClipboardFormatListener + message-only window
	ch := make(chan ClipData)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

func (c *windowsClipboard) SetText(text string) ([32]byte, error) {
	// TODO: Phase 1.3 — OpenClipboard + SetClipboardData(CF_UNICODETEXT)
	return [32]byte{}, errors.New("not implemented")
}

func (c *windowsClipboard) SetFileRefs(paths []string) ([32]byte, error) {
	// TODO: Phase 2.2 — CF_HDROP / DROPFILES
	return [32]byte{}, errors.New("not implemented")
}

func (c *windowsClipboard) Hash() ([32]byte, error) {
	// TODO: Phase 1.3
	return [32]byte{}, errors.New("not implemented")
}
