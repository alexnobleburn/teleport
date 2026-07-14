//go:build windows

package clipboard

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"unicode/utf16"
	"unsafe"
)

// DROPFILES layout (20 bytes):
//   pFiles  uint32  (offset to file list, always 20)
//   pt.x    int32   (0)
//   pt.y    int32   (0)
//   fNC     int32   (0)
//   fWide   int32   (1 = UTF-16)
const dropFilesSize = 20

// readFiles reads CF_HDROP from the clipboard and returns file metadata.
func (c *windowsClipboard) readFiles() ([]FileMeta, error) {
	r, _, _ := openClipboard.Call(0)
	if r == 0 {
		return nil, errors.New("OpenClipboard failed")
	}
	defer closeClipboard.Call()

	r, _, _ = isClipboardFormatAvailable.Call(cfHDROP)
	if r == 0 {
		return nil, nil
	}

	h, _, _ := getClipboardData.Call(cfHDROP)
	if h == 0 {
		return nil, nil
	}

	ptr, _, _ := globalLock.Call(h)
	if ptr == 0 {
		return nil, errors.New("GlobalLock failed")
	}
	defer globalUnlock.Call(h)

	sz, _, _ := globalSize.Call(h)
	if sz < dropFilesSize {
		return nil, errors.New("DROPFILES too small")
	}

	data := unsafe.Slice((*byte)(ptrToUnsafe(ptr)), sz)

	// Parse DROPFILES header
	pFiles := binary.LittleEndian.Uint32(data[0:4])
	fWide := binary.LittleEndian.Uint32(data[16:20])
	if fWide != 1 {
		return nil, errors.New("DROPFILES: ANSI not supported, expected UTF-16")
	}
	if pFiles > uint32(sz) {
		return nil, errors.New("DROPFILES: pFiles offset out of bounds")
	}

	// Parse UTF-16 paths: null-terminated strings, double-null at end
	paths := parseUTF16Paths(data[pFiles:])

	var files []FileMeta
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		files = append(files, FileMeta{
			Name:      filepath.Base(p),
			Size:      info.Size(),
			LocalPath: p,
			IsDir:     info.IsDir(),
		})
	}
	return files, nil
}

// parseUTF16Paths parses a sequence of null-terminated UTF-16 strings ending with double null.
func parseUTF16Paths(data []byte) []string {
	if len(data) < 4 {
		return nil
	}
	// Interpret as uint16 slice
	u16 := unsafe.Slice((*uint16)(unsafe.Pointer(&data[0])), len(data)/2)

	var paths []string
	start := 0
	for i := 0; i < len(u16); i++ {
		if u16[i] == 0 {
			if i == start {
				break // double null = end
			}
			paths = append(paths, string(utf16.Decode(u16[start:i])))
			start = i + 1
		}
	}
	return paths
}

// SetFileRefs creates CF_HDROP with DROPFILES structure and sets it in the clipboard.
func (c *windowsClipboard) setFileRefsImpl(paths []string) ([32]byte, error) {
	// Build DROPFILES + UTF-16 paths
	var pathData []uint16
	for _, p := range paths {
		pathData = append(pathData, utf16.Encode([]rune(p))...)
		pathData = append(pathData, 0) // null terminator
	}
	pathData = append(pathData, 0) // double null terminator

	totalSize := dropFilesSize + len(pathData)*2

	r, _, _ := openClipboard.Call(0)
	if r == 0 {
		return [32]byte{}, errors.New("OpenClipboard failed")
	}
	defer closeClipboard.Call()

	emptyClipboard.Call()

	h, _, _ := globalAlloc.Call(gmemMoveable, uintptr(totalSize))
	if h == 0 {
		return [32]byte{}, errors.New("GlobalAlloc failed")
	}

	ptr, _, _ := globalLock.Call(h)
	if ptr == 0 {
		globalFree.Call(h)
		return [32]byte{}, errors.New("GlobalLock failed")
	}

	mem := unsafe.Slice((*byte)(ptrToUnsafe(ptr)), totalSize)

	// DROPFILES header
	binary.LittleEndian.PutUint32(mem[0:4], dropFilesSize) // pFiles
	binary.LittleEndian.PutUint32(mem[4:8], 0)             // pt.x
	binary.LittleEndian.PutUint32(mem[8:12], 0)            // pt.y
	binary.LittleEndian.PutUint32(mem[12:16], 0)           // fNC
	binary.LittleEndian.PutUint32(mem[16:20], 1)           // fWide = TRUE

	// Copy UTF-16 path data
	dst := unsafe.Slice((*uint16)(unsafe.Pointer(&mem[dropFilesSize])), len(pathData))
	copy(dst, pathData)

	globalUnlock.Call(h)

	// After SetClipboardData, the OS owns the memory
	r, _, _ = setClipboardData.Call(cfHDROP, h)
	if r == 0 {
		globalFree.Call(h)
		return [32]byte{}, errors.New("SetClipboardData CF_HDROP failed")
	}

	hash := hashPaths(paths)
	c.mu.Lock()
	c.lastHash = hash
	c.mu.Unlock()

	return hash, nil
}

// hashPaths computes a deterministic hash from file paths for anti-loop.
// Reuses HashFiles to ensure consistent sorting with Watch.
func hashPaths(paths []string) [32]byte {
	files := make([]FileMeta, len(paths))
	for i, p := range paths {
		files[i] = FileMeta{LocalPath: p}
	}
	return HashFiles(files)
}
