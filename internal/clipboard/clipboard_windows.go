//go:build windows

package clipboard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	openClipboard              = user32.NewProc("OpenClipboard")
	closeClipboard             = user32.NewProc("CloseClipboard")
	getClipboardData           = user32.NewProc("GetClipboardData")
	setClipboardData           = user32.NewProc("SetClipboardData")
	emptyClipboard             = user32.NewProc("EmptyClipboard")
	isClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	addClipboardFormatListener = user32.NewProc("AddClipboardFormatListener")
	removeClipboardFormatListener = user32.NewProc("RemoveClipboardFormatListener")
	createWindowExW            = user32.NewProc("CreateWindowExW")
	registerClassExW           = user32.NewProc("RegisterClassExW")
	defWindowProcW             = user32.NewProc("DefWindowProcW")
	getMessageW                = user32.NewProc("GetMessageW")
	translateMessage           = user32.NewProc("TranslateMessage")
	dispatchMessageW           = user32.NewProc("DispatchMessageW")
	postMessageW               = user32.NewProc("PostMessageW")

	globalAlloc = kernel32.NewProc("GlobalAlloc")
	globalFree  = kernel32.NewProc("GlobalFree")
	globalLock  = kernel32.NewProc("GlobalLock")
	globalUnlock = kernel32.NewProc("GlobalUnlock")
	globalSize  = kernel32.NewProc("GlobalSize")
)

const (
	cfUnicodeText      = 13
	cfHDROP            = 15
	gmemMoveable       = 0x0002
	wmClipboardUpdate  = 0x031D
	wmQuit             = 0x0012
)

// WNDCLASSEXW size = 80 on 64-bit
type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      [2]int32
}

type windowsClipboard struct {
	logger   *slog.Logger
	lastHash [32]byte
	mu       sync.Mutex
	hwnd     uintptr
}

// New creates a Windows clipboard implementation.
// opts is ignored on Windows (poll interval is macOS-only; Windows uses events).
func New(logger *slog.Logger, opts ...time.Duration) Clipboard {
	return &windowsClipboard{logger: logger}
}

func (c *windowsClipboard) Watch(ctx context.Context) (<-chan ClipData, error) {
	ch := make(chan ClipData, 4)
	ready := make(chan error, 1)
	// loopStarted is closed after the message loop begins pumping,
	// providing deterministic synchronization instead of time.Sleep.
	loopStarted := make(chan struct{})

	go func() {
		// Win32 message loop MUST run on a locked OS thread.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(ch)

		hwnd, err := c.createHiddenWindow()
		if err != nil {
			ready <- err
			return
		}

		r, _, _ := addClipboardFormatListener.Call(hwnd)
		if r == 0 {
			ready <- errors.New("AddClipboardFormatListener failed")
			return
		}
		defer removeClipboardFormatListener.Call(hwnd)

		ready <- nil

		// Start ctx cancellation goroutine AFTER ready is sent,
		// and pass hwnd explicitly to avoid race.
		localHwnd := hwnd
		go func() {
			<-ctx.Done()
			postMessageW.Call(localHwnd, wmQuit, 0, 0)
		}()

		close(loopStarted)

		var m msg
		for {
			ret, _, _ := getMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if ret == 0 || int32(ret) == -1 {
				return
			}
			translateMessage.Call(uintptr(unsafe.Pointer(&m)))
			dispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))

			if m.message == wmClipboardUpdate {
				data, hash, err := c.readCurrent()
				if err != nil {
					c.logger.Debug("clipboard read failed", "error", err)
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
					// Drop if channel full
				}
			}
		}
	}()

	if err := <-ready; err != nil {
		return nil, err
	}
	<-loopStarted
	return ch, nil
}

func (c *windowsClipboard) createHiddenWindow() (uintptr, error) {
	className := utf16.Encode([]rune("TeleportClipboardWatcher\x00"))

	wndProc := syscall.NewCallback(func(hwnd, msg, wParam, lParam uintptr) uintptr {
		ret, _, _ := defWindowProcW.Call(hwnd, msg, wParam, lParam)
		return ret
	})

	wcx := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   wndProc,
		lpszClassName: &className[0],
	}

	registerClassExW.Call(uintptr(unsafe.Pointer(&wcx)))

	// Create a regular hidden window (hWndParent=0), NOT a message-only window.
	// AddClipboardFormatListener may not deliver WM_CLIPBOARDUPDATE to
	// message-only windows (HWND_MESSAGE).
	hwnd, _, err := createWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(&className[0])),
		0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, // hWndParent=0 (desktop), not HWND_MESSAGE
	)
	if hwnd == 0 {
		return 0, fmt.Errorf("CreateWindowExW: %w", err)
	}
	return hwnd, nil
}

func (c *windowsClipboard) readCurrent() (ClipData, [32]byte, error) {
	// CF_HDROP takes priority over CF_UNICODETEXT because Explorer sets both
	files, err := c.readFiles()
	if err == nil && len(files) > 0 {
		hash := HashFiles(files)
		return ClipData{Kind: KindFiles, Files: files}, hash, nil
	}

	text, err := c.readText()
	if err != nil {
		return ClipData{}, [32]byte{}, err
	}
	hash := HashText(text)
	return ClipData{Kind: KindText, Text: text}, hash, nil
}

func (c *windowsClipboard) readText() (string, error) {
	r, _, _ := openClipboard.Call(0)
	if r == 0 {
		return "", errors.New("OpenClipboard failed")
	}
	defer closeClipboard.Call()

	r, _, _ = isClipboardFormatAvailable.Call(cfUnicodeText)
	if r == 0 {
		return "", nil
	}

	h, _, _ := getClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", nil
	}

	ptr, _, _ := globalLock.Call(h)
	if ptr == 0 {
		return "", errors.New("GlobalLock failed")
	}
	defer globalUnlock.Call(h)

	sz, _, _ := globalSize.Call(h)
	if sz == 0 {
		return "", nil
	}

	// Read UTF-16 data
	u16 := unsafe.Slice((*uint16)(ptrToUnsafe(ptr)), sz/2)
	// Find null terminator
	var end int
	for end = 0; end < len(u16); end++ {
		if u16[end] == 0 {
			break
		}
	}
	return string(utf16.Decode(u16[:end])), nil
}

func (c *windowsClipboard) SetText(text string) ([32]byte, error) {
	r, _, _ := openClipboard.Call(0)
	if r == 0 {
		return [32]byte{}, errors.New("OpenClipboard failed")
	}
	defer closeClipboard.Call()

	emptyClipboard.Call()

	u16 := utf16.Encode([]rune(text))
	u16 = append(u16, 0) // null terminator
	size := len(u16) * 2

	h, _, _ := globalAlloc.Call(gmemMoveable, uintptr(size))
	if h == 0 {
		return [32]byte{}, errors.New("GlobalAlloc failed")
	}

	ptr, _, _ := globalLock.Call(h)
	if ptr == 0 {
		globalFree.Call(h)
		return [32]byte{}, errors.New("GlobalLock failed")
	}

	dst := unsafe.Slice((*uint16)(ptrToUnsafe(ptr)), len(u16))
	copy(dst, u16)
	globalUnlock.Call(h)

	// After SetClipboardData, the OS owns the memory — do NOT call GlobalFree.
	r, _, _ = setClipboardData.Call(cfUnicodeText, h)
	if r == 0 {
		globalFree.Call(h)
		return [32]byte{}, errors.New("SetClipboardData failed")
	}

	hash := HashText(text)
	c.mu.Lock()
	c.lastHash = hash
	c.mu.Unlock()

	return hash, nil
}

func (c *windowsClipboard) SetFileRefs(paths []string) ([32]byte, error) {
	return c.setFileRefsImpl(paths)
}

func (c *windowsClipboard) Hash() ([32]byte, error) {
	text, err := c.readText()
	if err != nil {
		return [32]byte{}, err
	}
	return HashText(text), nil
}

// ptrToUnsafe converts a uintptr (from Win32 syscall) to unsafe.Pointer.
// This is safe for pointers returned by GlobalLock and similar Win32 APIs
// that return actual memory addresses, not handles.
//
//go:nosplit
func ptrToUnsafe(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}

