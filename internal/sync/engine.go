package sync

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"teleport/internal/clipboard"
	"teleport/internal/discovery"
	"teleport/internal/staging"
	"teleport/internal/transport"
)

const maxDirFiles = 10000 // maximum files per directory to prevent resource exhaustion

// Engine is the main synchronization loop.
type Engine struct {
	clip      clipboard.Clipboard
	disc      discovery.Discovery
	listener  transport.Listener
	stager    *staging.Manager
	password  string
	name      string
	logger    *slog.Logger
	textOnly  bool
	localAddr string // if set, bind outgoing TCP to this IP (VPN bypass)

	lastSetHash [32]byte
	mu          sync.Mutex

	sender     transport.Sender
	senderAddr string
	connecting bool // true while connectToPeer is in progress
	senderMu   sync.Mutex
}

// New creates a new sync engine.
// localAddr is optional — if non-empty, outgoing TCP connections bind to this IP.
func New(
	clip clipboard.Clipboard,
	disc discovery.Discovery,
	listener transport.Listener,
	stager *staging.Manager,
	password, name string,
	textOnly bool,
	localAddr string,
	logger *slog.Logger,
) *Engine {
	return &Engine{
		clip:      clip,
		disc:      disc,
		listener:  listener,
		stager:    stager,
		password:  password,
		name:      name,
		textOnly:  textOnly,
		localAddr: localAddr,
		logger:    logger,
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
			err := e.listener.Accept(ctx,
				func() transport.ReceiveHandler {
					return &receiveHandler{engine: e}
				},
				func(sender transport.Sender) {
					// Inbound connection: use this sender for bidirectional communication
					e.senderMu.Lock()
					old := e.sender
					e.sender = sender
					e.senderAddr = "inbound"
					e.senderMu.Unlock()
					if old != nil {
						old.Close()
					}
					e.monitorSender(sender)
					e.logger.Info("using inbound connection for sending")
				},
			)
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
			go e.connectToPeer(ctx, peer)
		}
	}
}

func (e *Engine) connectToPeer(ctx context.Context, peer discovery.Peer) {
	// Skip if already connected or another connectToPeer is in progress
	e.senderMu.Lock()
	if e.sender != nil || e.connecting {
		e.senderMu.Unlock()
		return
	}
	e.connecting = true
	e.senderMu.Unlock()

	defer func() {
		e.senderMu.Lock()
		e.connecting = false
		e.senderMu.Unlock()
	}()

	e.logger.Info("connecting to peer", "name", peer.Name, "addr", peer.Addr)
	handler := &receiveHandler{engine: e}
	sender, err := transport.Dial(ctx, peer.Addr, e.password, e.localAddr, handler, e.logger)
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
	e.monitorSender(sender)
	e.logger.Info("connected", "peer", peer.Name, "addr", peer.Addr)
}

// disconnectPeer closes the current sender and resets it to nil.
// Discovery will detect sender==nil and attempt reconnection.
func (e *Engine) disconnectPeer() {
	e.senderMu.Lock()
	old := e.sender
	e.sender = nil
	e.senderAddr = ""
	e.senderMu.Unlock()
	if old != nil {
		old.Close()
		e.logger.Warn("disconnected from peer, waiting for discovery retry")
	}
}

// monitorSender watches for sender death (connection closed/broken).
// When the sender's Done() channel fires, resets e.sender to nil if it
// still points to the same sender — allowing discovery to reconnect.
func (e *Engine) monitorSender(s transport.Sender) {
	go func() {
		<-s.Done()
		e.senderMu.Lock()
		if e.sender == s {
			e.sender = nil
			e.senderAddr = ""
			e.senderMu.Unlock()
			e.logger.Warn("connection lost, waiting for reconnection")
		} else {
			e.senderMu.Unlock()
		}
	}()
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
			e.logger.Warn("send text failed, disconnecting", "error", err)
			e.disconnectPeer()
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
// Each file is hashed and sent from the same open handle to avoid TOCTOU issues.
// Directories are walked recursively — files inside get relative path names
// (e.g., "MyFolder/sub/file.txt") with forward slash as separator.
func (e *Engine) sendFiles(s transport.Sender, files []clipboard.FileMeta) {
	// Expand directories into individual files with relative paths.
	expanded := e.expandDirs(files)
	if len(expanded) == 0 {
		return
	}

	// Open, hash, and seek(0) each file. Same handle is used for sending.
	type fileInfo struct {
		name     string
		size     int64
		checksum [32]byte
		file     *os.File
	}
	var valid []fileInfo

	for _, f := range expanded {
		file, err := os.Open(f.path)
		if err != nil {
			e.logger.Warn("cannot open file for sending", "path", f.path, "error", err)
			continue
		}
		h := sha256.New()
		n, err := io.Copy(h, file)
		if err != nil {
			file.Close()
			e.logger.Warn("cannot hash file for sending", "path", f.path, "error", err)
			continue
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			file.Close()
			e.logger.Warn("cannot seek file for sending", "path", f.path, "error", err)
			continue
		}
		var checksum [32]byte
		copy(checksum[:], h.Sum(nil))
		valid = append(valid, fileInfo{name: f.name, size: n, checksum: checksum, file: file})
	}

	if len(valid) == 0 {
		return
	}

	// Build FileToSend using already-opened file handles.
	toSend := make([]transport.FileToSend, len(valid))
	for i, v := range valid {
		toSend[i] = transport.FileToSend{
			Name:     v.name,
			Size:     v.size,
			Checksum: v.checksum,
			Reader:   v.file,
		}
	}

	err := s.SendFiles(toSend)

	// Close all file handles.
	for _, v := range valid {
		v.file.Close()
	}

	if err != nil {
		e.logger.Warn("send files failed, disconnecting", "error", err)
		e.disconnectPeer()
		return
	}
	e.logger.Info("files synced", "count", len(toSend), "direction", "sent")
}

// expandedFile is a file ready to send with its protocol name and local path.
type expandedFile struct {
	name string // protocol name, may contain / for directory files
	size int64
	path string // local filesystem path
}

// errTooManyFiles is returned by expandDirs when a directory exceeds maxDirFiles.
var errTooManyFiles = fmt.Errorf("directory contains more than %d files", maxDirFiles)

// expandDirs expands directories into individual files with relative paths.
// Regular files are passed through as-is. Directory entries are walked recursively.
// Symlinks are skipped to prevent escaping the source directory.
// Names use forward slash as separator for cross-platform compatibility.
func (e *Engine) expandDirs(files []clipboard.FileMeta) []expandedFile {
	var result []expandedFile
	for _, f := range files {
		if !f.IsDir {
			result = append(result, expandedFile{
				name: f.Name,
				size: f.Size,
				path: f.LocalPath,
			})
			continue
		}
		// Walk directory recursively, skip symlinks
		dirName := f.Name
		dirPath := f.LocalPath
		count := 0
		err := filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				e.logger.Warn("walk error", "path", path, "error", err)
				return nil
			}
			// Skip symlinks to prevent following links outside the directory
			if d.Type()&os.ModeSymlink != 0 {
				e.logger.Debug("skipping symlink", "path", path)
				return nil
			}
			if d.IsDir() {
				return nil
			}
			count++
			if count > maxDirFiles {
				return errTooManyFiles
			}
			info, err := d.Info()
			if err != nil {
				e.logger.Warn("stat error", "path", path, "error", err)
				return nil
			}
			rel, err := filepath.Rel(dirPath, path)
			if err != nil {
				return nil
			}
			name := dirName + "/" + strings.ReplaceAll(rel, string(filepath.Separator), "/")
			result = append(result, expandedFile{
				name: name,
				size: info.Size(),
				path: path,
			})
			return nil
		})
		if err != nil {
			e.logger.Warn("walk directory failed", "dir", dirPath, "count", count, "error", err)
		} else {
			e.logger.Debug("directory expanded", "dir", dirName, "files", count)
		}
	}
	return result
}

