package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"
)

const handshakeTimeout = 5 * time.Second

type listenerImpl struct {
	ln        net.Listener
	masterKey [32]byte
	logger    *slog.Logger
}

// NewListener creates a TCP listener with AES-GCM encryption.
func NewListener(port int, password string, logger *slog.Logger) (Listener, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	return &listenerImpl{
		ln:        ln,
		masterKey: deriveKey(password),
		logger:    logger,
	}, nil
}

func (l *listenerImpl) Accept(ctx context.Context, newHandler func() ReceiveHandler) error {
	for {
		raw, err := l.ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			l.logger.Error("accept failed", "error", err)
			continue
		}
		handler := newHandler() // per-connection handler, no shared mutable state
		go l.handleConn(ctx, raw, handler)
	}
}

func (l *listenerImpl) handleConn(ctx context.Context, raw net.Conn, handler ReceiveHandler) {
	defer raw.Close()
	addr := raw.RemoteAddr().String()

	raw.SetDeadline(time.Now().Add(handshakeTimeout))
	sc, err := Handshake(raw, l.masterKey, false)
	if err != nil {
		l.logger.Warn("handshake failed", "addr", addr, "error", err)
		return
	}
	raw.SetDeadline(time.Time{})
	l.logger.Info("peer connected (inbound)", "addr", addr)

	l.readLoop(ctx, sc, handler)
}

// readLoop reads frames sequentially. File transfers are read synchronously
// (no goroutine) to avoid data races on SecureConn.recvSeq.
func (l *listenerImpl) readLoop(ctx context.Context, sc *SecureConn, handler ReceiveHandler) {
	for {
		if ctx.Err() != nil {
			return
		}

		msgType, payload, err := sc.ReadFrame()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return
			}
			l.logger.Warn("read frame failed", "error", err)
			return
		}

		switch msgType {
		case MsgText:
			handler.OnText(string(payload))

		case MsgFileHeader:
			l.handleFileTransfer(sc, handler, payload)

		case MsgBatchBegin:
			if len(payload) >= 4 {
				count := int(binary.BigEndian.Uint32(payload[:4]))
				handler.OnBatchBegin(count)
			}

		case MsgBatchEnd:
			handler.OnBatchEnd()

		case MsgPing:
			// Pong is written from the read loop goroutine. This is safe because
			// the read loop is the only goroutine that uses this SecureConn —
			// the listener does NOT create a Sender on accepted connections.
			// The outbound Sender (from Dial) uses a separate SecureConn.
			_ = sc.WriteFrame(MsgPong, nil)

		case MsgPong:
			// Handled by ping logic

		default:
			l.logger.Debug("unknown message type", "type", uint8(msgType))
		}
	}
}

// handleFileTransfer reads file chunks synchronously from sc (no goroutine).
// This avoids data races on SecureConn.recvSeq — only one goroutine reads at a time.
func (l *listenerImpl) handleFileTransfer(sc *SecureConn, handler ReceiveHandler, headerPayload []byte) {
	if len(headerPayload) < 4 {
		l.logger.Warn("invalid file header: too short")
		return
	}
	nameLen := int(binary.BigEndian.Uint32(headerPayload[:4]))
	if len(headerPayload) < 4+nameLen+8+32 {
		l.logger.Warn("invalid file header: truncated")
		return
	}
	name := string(headerPayload[4 : 4+nameLen])
	size := int64(binary.BigEndian.Uint64(headerPayload[4+nameLen:]))
	var checksum [32]byte
	copy(checksum[:], headerPayload[4+nameLen+8:])

	// Read all chunks synchronously into a pipe.
	// The pipe writer runs inline (same goroutine as readLoop),
	// and handler.OnFile reads from the pipe reader in a separate goroutine.
	pr, pw := io.Pipe()

	// Handler reads from pipe in background
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.OnFile(name, size, checksum, pr)
	}()

	// Read chunks inline (synchronous — no race on recvSeq)
	for {
		msgType, payload, err := sc.ReadFrame()
		if err != nil {
			pw.CloseWithError(fmt.Errorf("read chunk: %w", err))
			<-done
			return
		}
		if msgType == MsgFileDone {
			pw.Close()
			<-done
			return
		}
		if msgType != MsgFileChunk {
			pw.CloseWithError(fmt.Errorf("unexpected message type during file transfer: 0x%02x", msgType))
			<-done
			return
		}
		if _, err := pw.Write(payload); err != nil {
			// Handler closed the reader early
			<-done
			return
		}
	}
}

func (l *listenerImpl) Addr() string {
	return l.ln.Addr().String()
}

func (l *listenerImpl) Close() error {
	return l.ln.Close()
}

// Dial connects to a peer and performs handshake as initiator.
func Dial(addr, password string, logger *slog.Logger) (Sender, error) {
	raw, err := net.DialTimeout("tcp", addr, handshakeTimeout)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	masterKey := deriveKey(password)
	raw.SetDeadline(time.Now().Add(handshakeTimeout))
	sc, err := Handshake(raw, masterKey, true)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("handshake with %s: %w", addr, err)
	}
	raw.SetDeadline(time.Time{})

	return NewSender(sc, logger), nil
}
