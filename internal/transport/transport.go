package transport

import (
	"context"
	"io"
)

// MsgType identifies the type of protocol message.
type MsgType byte

const (
	MsgText       MsgType = 0x01
	MsgFileHeader MsgType = 0x02
	MsgFileChunk  MsgType = 0x03
	MsgFileDone   MsgType = 0x04
	MsgPing       MsgType = 0x05
	MsgPong       MsgType = 0x06
	MsgBatchBegin MsgType = 0x07 // batch start, payload: uint32 file count
	MsgBatchEnd   MsgType = 0x08 // batch end, payload: empty
)

// FileToSend describes a file to be transmitted.
type FileToSend struct {
	Name     string
	Size     int64
	Checksum [32]byte
	Reader   io.Reader
}

// Sender serializes all sends through a single-writer goroutine.
// This prevents interleaving of multi-frame operations (SendFile = Header + N Chunks + Done).
// All Send* methods block until the send completes.
type Sender interface {
	SendText(text string) error
	SendFile(name string, size int64, checksum [32]byte, r io.Reader) error
	// SendFiles sends a batch: MsgBatchBegin(count) -> [files] -> MsgBatchEnd.
	// Receiver calls SetFileRefs only after MsgBatchEnd (atomicity).
	SendFiles(files []FileToSend) error
	Close() error
}

// Listener accepts incoming encrypted TCP connections.
type Listener interface {
	// Accept accepts incoming connections. For each connection, calls newHandler()
	// to create a per-connection ReceiveHandler (avoids shared mutable state).
	// Blocking, cancelled via ctx.
	Accept(ctx context.Context, newHandler func() ReceiveHandler) error
	// Addr returns the listen address (host:port).
	Addr() string
	Close() error
}

// ReceiveHandler is called for each received message.
type ReceiveHandler interface {
	OnText(text string)
	OnFile(name string, size int64, checksum [32]byte, r io.Reader)
	// OnBatchBegin is called on MsgBatchBegin. count is the number of files in the batch.
	OnBatchBegin(count int)
	// OnBatchEnd is called on MsgBatchEnd. Receiver should call SetFileRefs
	// with all accumulated staged paths.
	OnBatchEnd()
}
