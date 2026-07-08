//go:build darwin

package clipboard

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit

#import <AppKit/AppKit.h>
#include <stdlib.h>

const char* readClipboardFiles() {
    @autoreleasepool {
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        NSArray *files = [pb propertyListForType:NSFilenamesPboardType];
        if (files == nil || [files count] == 0) return NULL;
        NSData *json = [NSJSONSerialization dataWithJSONObject:files options:0 error:nil];
        if (json == nil) return NULL;
        NSString *str = [[NSString alloc] initWithData:json encoding:NSUTF8StringEncoding];
        return strdup([str UTF8String]);
    }
}

void writeClipboardFiles(const char *jsonPaths) {
    @autoreleasepool {
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        [pb clearContents];
        NSData *data = [[NSString stringWithUTF8String:jsonPaths] dataUsingEncoding:NSUTF8StringEncoding];
        NSArray *paths = [NSJSONSerialization JSONObjectWithData:data options:0 error:nil];
        if (paths != nil) {
            [pb setPropertyList:paths forType:NSFilenamesPboardType];
        }
    }
}
*/
import "C"

import (
	"encoding/json"
	"os"
	"path/filepath"
	"unsafe"
)

// readFiles reads NSFilenamesPboardType from the pasteboard.
func (c *darwinClipboard) readFilePaths() ([]string, error) {
	cstr := C.readClipboardFiles()
	if cstr == nil {
		return nil, nil
	}
	defer C.free(unsafe.Pointer(cstr))

	jsonStr := C.GoString(cstr)
	var paths []string
	if err := json.Unmarshal([]byte(jsonStr), &paths); err != nil {
		return nil, err
	}
	return paths, nil
}

// readFilesMeta returns FileMeta for files in the clipboard.
func (c *darwinClipboard) readFilesMeta() ([]FileMeta, error) {
	paths, err := c.readFilePaths()
	if err != nil || len(paths) == 0 {
		return nil, err
	}

	var files []FileMeta
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			continue
		}
		files = append(files, FileMeta{
			Name:      filepath.Base(p),
			Size:      info.Size(),
			LocalPath: p,
		})
	}
	return files, nil
}

// setFileRefsImpl writes file paths to NSFilenamesPboardType.
func (c *darwinClipboard) setFileRefsImpl(paths []string) ([32]byte, error) {
	jsonBytes, err := json.Marshal(paths)
	if err != nil {
		return [32]byte{}, err
	}

	cstr := C.CString(string(jsonBytes))
	defer C.free(unsafe.Pointer(cstr))
	C.writeClipboardFiles(cstr)

	hash := hashPathsDarwin(paths)
	c.mu.Lock()
	c.lastHash = hash
	c.mu.Unlock()
	c.updateChangeCount()

	return hash, nil
}

// hashPathsDarwin computes a deterministic hash from file paths for anti-loop.
// Reuses HashFiles to ensure consistent sorting with Watch.
func hashPathsDarwin(paths []string) [32]byte {
	files := make([]FileMeta, len(paths))
	for i, p := range paths {
		files[i] = FileMeta{LocalPath: p}
	}
	return HashFiles(files)
}
