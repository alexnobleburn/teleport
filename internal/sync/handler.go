package sync

import (
	"io"
)

// receiveHandler implements transport.ReceiveHandler.
type receiveHandler struct {
	engine     *Engine
	batchPaths []string
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
		h.batchPaths = append(h.batchPaths, stagedPath)
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
	h.engine.logger.Info("batch begin", "files", count)
}

func (h *receiveHandler) OnBatchEnd() {
	h.inBatch = false
	if len(h.batchPaths) == 0 {
		return
	}
	hash, err := h.engine.clip.SetFileRefs(h.batchPaths)
	if err != nil {
		h.engine.logger.Error("failed to set file refs", "error", err)
		return
	}
	h.engine.mu.Lock()
	h.engine.lastSetHash = hash
	h.engine.mu.Unlock()
	h.engine.logger.Info("batch synced", "files", len(h.batchPaths), "direction", "received")
	h.batchPaths = nil
}
