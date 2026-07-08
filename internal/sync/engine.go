package sync

import (
	"context"
	"log/slog"
	"sync"

	"teleport/internal/clipboard"
	"teleport/internal/discovery"
	"teleport/internal/transport"
)

// Engine is the main synchronization loop.
// It watches the clipboard, discovers peers, and sends/receives clipboard data.
type Engine struct {
	clip     clipboard.Clipboard
	disc     discovery.Discovery
	listener transport.Listener
	password string
	name     string
	logger   *slog.Logger
	textOnly bool

	// lastSetHash is the hash of content we programmatically set in the clipboard.
	// Used for anti-loop: we ignore clipboard changes matching this hash.
	lastSetHash [32]byte
	mu          sync.Mutex

	sender   transport.Sender
	senderMu sync.Mutex
}

// New creates a new sync engine.
func New(
	clip clipboard.Clipboard,
	disc discovery.Discovery,
	listener transport.Listener,
	password, name string,
	textOnly bool,
	logger *slog.Logger,
) *Engine {
	return &Engine{
		clip:     clip,
		disc:     disc,
		listener: listener,
		password: password,
		name:     name,
		textOnly: textOnly,
		logger:   logger,
	}
}

// Run starts the sync loop. Blocking, cancelled via ctx.
func (e *Engine) Run(ctx context.Context) error {
	// TODO: Phase 1.4 — start listener, discovery, clipboard watch
	e.logger.Info("engine running", "name", e.name)
	<-ctx.Done()
	return ctx.Err()
}
