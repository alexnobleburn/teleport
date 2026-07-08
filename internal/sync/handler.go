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
	// TODO: Phase 2 — stage file, accumulate paths for batch
	h.engine.logger.Warn("file receive not implemented yet", "name", name, "size", size)
	n, _ := io.Copy(io.Discard, r)
	h.engine.logger.Debug("file data drained", "bytes_discarded", n)
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
	h.engine.logger.Info("batch synced", "files", len(h.batchPaths))
	h.batchPaths = nil
}
