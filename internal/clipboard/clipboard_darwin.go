//go:build darwin

package clipboard

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit

#import <AppKit/AppKit.h>
#include <stdlib.h>

const char* readClipboardText() {
    @autoreleasepool {
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        NSString *text = [pb stringForType:NSPasteboardTypeString];
        if (text == nil) return NULL;
        return strdup([text UTF8String]);
    }
}

void writeClipboardText(const char *text) {
    @autoreleasepool {
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        [pb clearContents];
        [pb setString:[NSString stringWithUTF8String:text] forType:NSPasteboardTypeString];
    }
}

long getChangeCount() {
    return [[NSPasteboard generalPasteboard] changeCount];
}
*/
import "C"

import (
	"context"
	"log/slog"
	"sync"
	"time"
	"unsafe"
)

type darwinClipboard struct {
	logger          *slog.Logger
	lastHash        [32]byte
	lastChangeCount int64
	pollInterval    time.Duration
	mu              sync.Mutex
}

// New creates a macOS clipboard implementation.
// opts[0] is poll interval (default 300ms).
func New(logger *slog.Logger, opts ...time.Duration) Clipboard {
	interval := 300 * time.Millisecond
	if len(opts) > 0 && opts[0] > 0 {
		interval = opts[0]
	}
	return &darwinClipboard{
		logger:       logger,
		pollInterval: interval,
	}
}

func (c *darwinClipboard) Watch(ctx context.Context) (<-chan ClipData, error) {
	c.lastChangeCount = int64(C.getChangeCount())

	ch := make(chan ClipData, 4)
	go func() {
		defer close(ch)
		ticker := time.NewTicker(c.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cc := int64(C.getChangeCount())
				if cc == c.lastChangeCount {
					continue
				}
				c.lastChangeCount = cc

				// Check files first (Finder sets both file refs and text)
				data, hash := c.readCurrent()
				if data.Kind == KindText && data.Text == "" {
					continue
				}

				c.mu.Lock()
				if hash == c.lastHash {
					c.mu.Unlock()
					continue
				}
				c.lastHash = hash
				c.mu.Unlock()

				select {
				case ch <- data:
				default:
				}
			}
		}
	}()
	return ch, nil
}

func (c *darwinClipboard) readCurrent() (ClipData, [32]byte) {
	// Files take priority (Finder sets both file refs and text)
	files, err := c.readFilesMeta()
	if err == nil && len(files) > 0 {
		hash := HashFiles(files)
		return ClipData{Kind: KindFiles, Files: files}, hash
	}

	text := c.readText()
	hash := HashText(text)
	return ClipData{Kind: KindText, Text: text}, hash
}

func (c *darwinClipboard) readText() string {
	cstr := C.readClipboardText()
	if cstr == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(cstr))
	return C.GoString(cstr)
}

func (c *darwinClipboard) SetText(text string) ([32]byte, error) {
	cstr := C.CString(text)
	defer C.free(unsafe.Pointer(cstr))
	C.writeClipboardText(cstr)

	hash := HashText(text)
	c.mu.Lock()
	c.lastHash = hash
	c.lastChangeCount = int64(C.getChangeCount())
	c.mu.Unlock()

	return hash, nil
}

func (c *darwinClipboard) SetFileRefs(paths []string) ([32]byte, error) {
	return c.setFileRefsImpl(paths)
}

func (c *darwinClipboard) Hash() ([32]byte, error) {
	_, hash := c.readCurrent()
	return hash, nil
}

// updateChangeCount snapshots the current NSPasteboard changeCount.
// Called from darwin_files.go after modifying the pasteboard.
func (c *darwinClipboard) updateChangeCount() {
	c.lastChangeCount = int64(C.getChangeCount())
}

