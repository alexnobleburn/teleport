package transport

import (
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

const sendQueueSize = 16

// sendOp represents a single atomic send operation.
type sendOp struct {
	fn   func() error
	done chan error
}

type senderImpl struct {
	conn   *SecureConn
	logger *slog.Logger
	sendCh chan sendOp
	closed chan struct{}
	once   sync.Once
}

// NewSender creates a Sender that serializes all writes through a single-writer goroutine.
func NewSender(conn *SecureConn, logger *slog.Logger) Sender {
	s := &senderImpl{
		conn:   conn,
		logger: logger,
		sendCh: make(chan sendOp, sendQueueSize),
		closed: make(chan struct{}),
	}
	go s.writeLoop()
	return s
}

func (s *senderImpl) writeLoop() {
	defer close(s.closed)
	for op := range s.sendCh {
		op.done <- s.safeExec(op.fn)
	}
}

func (s *senderImpl) safeExec(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("writeLoop panic: %v", r)
			s.logger.Error("writeLoop panic", "error", r)
		}
	}()
	return fn()
}

func (s *senderImpl) do(fn func() error) error {
	op := sendOp{fn: fn, done: make(chan error, 1)}
	select {
	case s.sendCh <- op:
		return <-op.done
	case <-s.closed:
		return fmt.Errorf("sender closed")
	}
}

func (s *senderImpl) SendText(text string) error {
	return s.do(func() error {
		return s.conn.WriteFrame(MsgText, []byte(text))
	})
}

func (s *senderImpl) SendFile(name string, size int64, checksum [32]byte, r io.Reader) error {
	return s.do(func() error {
		return s.sendFileFrames(name, size, checksum, r)
	})
}

func (s *senderImpl) SendFiles(files []FileToSend) error {
	return s.do(func() error {
		// BatchBegin: uint32 count
		var countBuf [4]byte
		binary.BigEndian.PutUint32(countBuf[:], uint32(len(files)))
		if err := s.conn.WriteFrame(MsgBatchBegin, countBuf[:]); err != nil {
			return fmt.Errorf("batch begin: %w", err)
		}

		for _, f := range files {
			if err := s.sendFileFrames(f.Name, f.Size, f.Checksum, f.Reader); err != nil {
				return fmt.Errorf("batch file %q: %w", f.Name, err)
			}
		}

		if err := s.conn.WriteFrame(MsgBatchEnd, nil); err != nil {
			return fmt.Errorf("batch end: %w", err)
		}
		return nil
	})
}

func (s *senderImpl) sendFileFrames(name string, size int64, checksum [32]byte, r io.Reader) error {
	// FileHeader: [4-byte name length][name][8-byte size][32-byte checksum]
	nameBytes := []byte(name)
	header := make([]byte, 4+len(nameBytes)+8+32)
	binary.BigEndian.PutUint32(header[0:4], uint32(len(nameBytes)))
	copy(header[4:4+len(nameBytes)], nameBytes)
	binary.BigEndian.PutUint64(header[4+len(nameBytes):], uint64(size))
	copy(header[4+len(nameBytes)+8:], checksum[:])

	if err := s.conn.WriteFrame(MsgFileHeader, header); err != nil {
		return fmt.Errorf("file header: %w", err)
	}

	// File chunks
	buf := make([]byte, FileChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if writeErr := s.conn.WriteFrame(MsgFileChunk, buf[:n]); writeErr != nil {
				return fmt.Errorf("file chunk: %w", writeErr)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read file: %w", err)
		}
	}

	// FileDone
	if err := s.conn.WriteFrame(MsgFileDone, nil); err != nil {
		return fmt.Errorf("file done: %w", err)
	}
	return nil
}

// Close stops the write loop and closes the underlying connection. Idempotent.
func (s *senderImpl) Close() error {
	var err error
	s.once.Do(func() {
		close(s.sendCh)
		<-s.closed // wait for writeLoop to finish
		err = s.conn.raw.Close()
	})
	return err
}

// Done returns a channel that is closed when the sender's write loop has stopped.
func (s *senderImpl) Done() <-chan struct{} {
	return s.closed
}
