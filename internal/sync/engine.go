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
type Engine struct {
	clip     clipboard.Clipboard
	disc     discovery.Discovery
	listener transport.Listener
	password string
	name     string
	logger   *slog.Logger
	textOnly bool

	lastSetHash [32]byte
	mu          sync.Mutex

	sender      transport.Sender
	senderAddr  string // address of connected peer (for dedup)
	senderMu    sync.Mutex
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
// If any critical component fails (clipboard, listener), the engine stops.
func (e *Engine) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	fatalErr := make(chan error, 4)

	// 1. Listener: accept incoming TCP connections
	if e.listener != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.listener.Accept(ctx, &receiveHandler{engine: e}); err != nil {
				if ctx.Err() == nil {
					e.logger.Error("listener failed", "error", err)
					fatalErr <- err
					cancel()
				}
			}
		}()
	}

	// 2. Discovery Announce
	if e.disc != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.disc.Announce(ctx); err != nil && ctx.Err() == nil {
				e.logger.Debug("announce stopped", "error", err)
			}
		}()
	}

	// 3. Discovery: discover peers and connect
	if e.disc != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.discoverLoop(ctx); err != nil && ctx.Err() == nil {
				e.logger.Debug("discover stopped", "error", err)
			}
		}()
	}

	// 4. Clipboard Watch: monitor and send changes
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := e.clipboardLoop(ctx); err != nil {
			if ctx.Err() == nil {
				e.logger.Error("clipboard watch failed", "error", err)
				fatalErr <- err
				cancel()
			}
		}
	}()

	wg.Wait()

	select {
	case err := <-fatalErr:
		return err
	default:
		return ctx.Err()
	}
}

func (e *Engine) discoverLoop(ctx context.Context) error {
	peers, err := e.disc.Discover(ctx)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case peer, ok := <-peers:
			if !ok {
				return nil
			}
			go e.connectToPeer(peer)
		}
	}
}

func (e *Engine) connectToPeer(peer discovery.Peer) {
	// Dedup: skip if already connected to this address
	e.senderMu.Lock()
	if e.senderAddr == peer.Addr && e.sender != nil {
		e.senderMu.Unlock()
		return
	}
	e.senderMu.Unlock()

	e.logger.Info("connecting to peer", "name", peer.Name, "addr", peer.Addr)
	sender, err := transport.Dial(peer.Addr, e.password, e.logger)
	if err != nil {
		e.logger.Warn("failed to connect to peer", "name", peer.Name, "error", err)
		return
	}

	e.senderMu.Lock()
	old := e.sender
	e.sender = sender
	e.senderAddr = peer.Addr
	e.senderMu.Unlock()

	if old != nil {
		old.Close()
	}
	e.logger.Info("connected", "peer", peer.Name, "addr", peer.Addr)
}

func (e *Engine) clipboardLoop(ctx context.Context) error {
	changes, err := e.clip.Watch(ctx)
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case data, ok := <-changes:
			if !ok {
				return nil
			}
			e.handleClipboardChange(data)
		}
	}
}

func (e *Engine) handleClipboardChange(data clipboard.ClipData) {
	var hash [32]byte
	switch data.Kind {
	case clipboard.KindText:
		hash = clipboard.HashText(data.Text)
	case clipboard.KindFiles:
		hash = clipboard.HashFiles(data.Files)
	}

	// Anti-loop: skip if this is content we set ourselves
	e.mu.Lock()
	if hash == e.lastSetHash {
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()

	e.senderMu.Lock()
	s := e.sender
	e.senderMu.Unlock()

	if s == nil {
		e.logger.Debug("no peer connected, skipping")
		return
	}

	switch data.Kind {
	case clipboard.KindText:
		if err := s.SendText(data.Text); err != nil {
			e.logger.Error("send text failed", "error", err)
			return
		}
		e.logger.Info("text synced", "bytes", len(data.Text), "direction", "sent")

	case clipboard.KindFiles:
		if e.textOnly {
			return
		}
		// TODO: Phase 2 — send files
		e.logger.Warn("file sync not implemented yet", "count", len(data.Files))
	}
}

// setSender is called by the listener when an inbound connection establishes.
func (e *Engine) setSender(s transport.Sender) {
	e.senderMu.Lock()
	old := e.sender
	e.sender = s
	e.senderAddr = ""
	e.senderMu.Unlock()
	if old != nil {
		old.Close()
	}
}
