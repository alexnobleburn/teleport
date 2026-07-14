package sync

import (
	"io"
	"path/filepath"
	"strings"
)

// receiveHandler implements transport.ReceiveHandler.
type receiveHandler struct {
	engine     *Engine
	batchPaths []string // staged paths for flat files (no directory structure)
	batchRoots []string // staged root directory paths (for directory files)
	rootsSeen  map[string]bool
	inBatch    bool
}

func (h *receiveHandler) OnText(text string) {
	hash, err := h.engine.clip.SetText(text)
	if err != nil {
		h.engine.logger.Error("failed to set clipboard text", "error", err)
		return
	}
	h.engine.mu.Lock()
	h.engine.lastSetHash = hash
	h.engine.mu.Unlock()
	h.engine.logger.Info("text synced", "bytes", len(text), "direction", "received")
}

func (h *receiveHandler) OnFile(name string, size int64, checksum [32]byte, r io.Reader) {
	if h.engine.stager == nil {
		h.engine.logger.Warn("staging not configured, discarding file", "name", name)
		io.Copy(io.Discard, r)
		return
	}

	stagedPath, err := h.engine.stager.Stage(name, size, checksum, r)
	if err != nil {
		h.engine.logger.Error("failed to stage file", "name", name, "error", err)
		return
	}

	if h.inBatch {
		// Check if this is a directory file (name contains /)
		if strings.Contains(name, "/") {
			// Extract root directory name and derive staged root from the actual staged path.
			// This handles collision suffixes correctly (uniquePath may rename the leaf).
			root := strings.SplitN(name, "/", 2)[0]
			if !h.rootsSeen[root] {
				h.rootsSeen[root] = true
				// Walk up from staged file path to find the root dir under staging
				// stagedPath = .../staged/Root/sub/file.txt → we need .../staged/Root
				stagingBase := h.engine.stager.Path()
				rel, _ := filepath.Rel(stagingBase, stagedPath)
				rootDir := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
				rootPath := filepath.Join(stagingBase, rootDir)
				h.batchRoots = append(h.batchRoots, rootPath)
			}
		} else {
			h.batchPaths = append(h.batchPaths, stagedPath)
		}
		return
	}

	// Single file (not in batch): set file refs immediately
	hash, err := h.engine.clip.SetFileRefs([]string{stagedPath})
	if err != nil {
		h.engine.logger.Error("failed to set file ref", "error", err)
		return
	}
	h.engine.mu.Lock()
	h.engine.lastSetHash = hash
	h.engine.mu.Unlock()
	h.engine.logger.Info("file synced", "name", name, "direction", "received")
}

func (h *receiveHandler) OnBatchBegin(count int) {
	h.inBatch = true
	h.batchPaths = make([]string, 0, count)
	h.batchRoots = nil
	h.rootsSeen = make(map[string]bool)
	h.engine.logger.Info("batch begin", "files", count)
}

func (h *receiveHandler) OnBatchEnd() {
	h.inBatch = false

	// Combine directory root paths and flat file paths for SetFileRefs
	allPaths := make([]string, 0, len(h.batchRoots)+len(h.batchPaths))
	allPaths = append(allPaths, h.batchRoots...)
	allPaths = append(allPaths, h.batchPaths...)
	if len(allPaths) == 0 {
		return
	}

	hash, err := h.engine.clip.SetFileRefs(allPaths)
	if err != nil {
		h.engine.logger.Error("failed to set file refs", "error", err)
		return
	}
	h.engine.mu.Lock()
	h.engine.lastSetHash = hash
	h.engine.mu.Unlock()
	h.engine.logger.Info("batch synced", "files", len(allPaths), "direction", "received")
	h.batchPaths = nil
	h.batchRoots = nil
	h.rootsSeen = nil
}
