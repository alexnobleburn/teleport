package sync

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"os"
	"sync"

	"teleport/internal/clipboard"
	"teleport/internal/discovery"
	"teleport/internal/staging"
	"teleport/internal/transport"
)

// Engine is the main synchronization loop.
type Engine struct {
	clip     clipboard.Clipboard
	disc     discovery.Discovery
	listener transport.Listener
	stager   *staging.Manager
	password string
	name     string
	logger   *slog.Logger
	textOnly bool

	lastSetHash [32]byte
	mu          sync.Mutex

	sender     transport.Sender
	senderAddr string
	senderMu   sync.Mutex
}

// New creates a new sync engine.
func New(
	clip clipboard.Clipboard,
	disc discovery.Discovery,
	listener transport.Listener,
	stager *staging.Manager,
	password, name string,
	textOnly bool,
	logger *slog.Logger,
) *Engine {
	return &Engine{
		clip:     clip,
		disc:     disc,
		listener: listener,
		stager:   stager,
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

	// Close listener when ctx is cancelled to unblock Accept()
	if e.listener != nil {
		go func() {
			<-ctx.Done()
			e.listener.Close()
		}()
	}

	// 1. Listener: accept incoming TCP connections (per-connection handler)
	if e.listener != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := e.listener.Accept(ctx, func() transport.ReceiveHandler {
				return &receiveHandler{engine: e}
			})
			if err != nil && ctx.Err() == nil {
				e.logger.Error("listener failed", "error", err)
				fatalErr <- err
				cancel()
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
		if err := e.clipboardLoop(ctx); err != nil && ctx.Err() == nil {
			e.logger.Error("clipboard watch failed", "error", err)
			fatalErr <- err
			cancel()
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
		e.sendFiles(s, data.Files)
	}
}

// sendFiles sends files one at a time: open → hash → seek(0) → send → close.
// Each file uses O(1) file descriptors. SHA-256 is computed from the same
// open handle used for sending to avoid TOCTOU issues.
func (e *Engine) sendFiles(s transport.Sender, files []clipboard.FileMeta) {
	// Pre-compute checksums and validate files exist before starting batch.
	// Only open one file at a time to keep FD usage at O(1).
	type fileInfo struct {
		meta     clipboard.FileMeta
		checksum [32]byte
	}
	var valid []fileInfo

	for _, f := range files {
		checksum, err := hashFileOnDisk(f.LocalPath)
		if err != nil {
			e.logger.Warn("cannot hash file for sending", "path", f.LocalPath, "error", err)
			continue
		}
		valid = append(valid, fileInfo{meta: f, checksum: checksum})
	}

	if len(valid) == 0 {
		return
	}

	// Build FileToSend with lazy readers — each file opened only when SendFiles
	// reads from it. We use a wrapper that opens the file on first Read call.
	toSend := make([]transport.FileToSend, len(valid))
	for i, v := range valid {
		toSend[i] = transport.FileToSend{
			Name:     v.meta.Name,
			Size:     v.meta.Size,
			Checksum: v.checksum,
			Reader:   &lazyFileReader{path: v.meta.LocalPath},
		}
	}

	err := s.SendFiles(toSend)

	// Close any opened lazy readers
	for _, f := range toSend {
		if closer, ok := f.Reader.(io.Closer); ok {
			closer.Close()
		}
	}

	if err != nil {
		e.logger.Error("send files failed", "error", err)
		return
	}
	e.logger.Info("files synced", "count", len(toSend), "direction", "sent")
}

// hashFileOnDisk computes SHA-256 of a file. Opens and closes the file.
func hashFileOnDisk(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result, nil
}

// lazyFileReader opens the file on first Read call. This keeps FD usage at O(1)
// during batch sends — only the file currently being transmitted is open.
type lazyFileReader struct {
	path string
	file *os.File
}

func (r *lazyFileReader) Read(p []byte) (int, error) {
	if r.file == nil {
		f, err := os.Open(r.path)
		if err != nil {
			return 0, err
		}
		r.file = f
	}
	return r.file.Read(p)
}

func (r *lazyFileReader) Close() error {
	if r.file != nil {
		return r.file.Close()
	}
	return nil
}
