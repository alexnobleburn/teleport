package transport

import (
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
)

// SecureConn wraps a net.Conn with AES-256-GCM encryption per frame.
//
// Threading contract:
//   - WRITES: must be serialized by caller. Outbound connections use Sender's
//     single-writer goroutine. Inbound connections write only MsgPong from
//     the read loop — no Sender is created on accepted connections.
//   - READS: must be serialized by caller. The read loop (listenerImpl.readLoop)
//     is the sole reader, including file chunk reads in handleFileTransfer.
//
// SecureConn itself has NO mutexes.
//
// Nonce isolation: initiator and acceptor use different nonce prefixes
// (0x00000000 and 0x00000001) to prevent nonce reuse on bidirectional connections
// that share the same AES-GCM key.
type SecureConn struct {
	raw        net.Conn
	aead       cipher.AEAD
	sendSeq    uint64
	recvSeq    uint64
	sendPrefix uint32 // nonce prefix for outgoing frames
	recvPrefix uint32 // nonce prefix for incoming frames
}

func newSecureConn(raw net.Conn, aead cipher.AEAD, initiator bool) *SecureConn {
	sc := &SecureConn{raw: raw, aead: aead, sendSeq: 1, recvSeq: 1}
	if initiator {
		sc.sendPrefix = 0
		sc.recvPrefix = 1
	} else {
		sc.sendPrefix = 1
		sc.recvPrefix = 0
	}
	return sc
}

// WriteFrame encrypts and sends a single frame: [4-byte length][encrypted payload].
func (sc *SecureConn) WriteFrame(msgType MsgType, payload []byte) error {
	plaintext := make([]byte, 1+len(payload))
	plaintext[0] = byte(msgType)
	copy(plaintext[1:], payload)

	nonce := makeNonce(sc.sendSeq, sc.sendPrefix)
	sc.sendSeq++

	ciphertext := sc.aead.Seal(nil, nonce[:], plaintext, nil)

	if len(ciphertext) > MaxFrameSize {
		return fmt.Errorf("frame too large: %d > %d", len(ciphertext), MaxFrameSize)
	}

	var header [FrameHeaderSize]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(ciphertext)))
	if _, err := sc.raw.Write(header[:]); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := sc.raw.Write(ciphertext); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}
	return nil
}

// ReadFrame reads and decrypts a single frame. Returns MsgType and decrypted payload.
func (sc *SecureConn) ReadFrame() (MsgType, []byte, error) {
	var header [FrameHeaderSize]byte
	if _, err := io.ReadFull(sc.raw, header[:]); err != nil {
		return 0, nil, fmt.Errorf("read header: %w", err)
	}
	size := binary.BigEndian.Uint32(header[:])
	if size > uint32(MaxFrameSize) {
		return 0, nil, fmt.Errorf("frame too large: %d > %d", size, MaxFrameSize)
	}
	if size == 0 {
		return 0, nil, errors.New("empty frame")
	}

	ciphertext := make([]byte, size)
	if _, err := io.ReadFull(sc.raw, ciphertext); err != nil {
		return 0, nil, fmt.Errorf("read payload: %w", err)
	}

	nonce := makeNonce(sc.recvSeq, sc.recvPrefix)
	sc.recvSeq++

	plaintext, err := sc.aead.Open(nil, nonce[:], ciphertext, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("decrypt: %w", err)
	}
	if len(plaintext) == 0 {
		return 0, nil, errors.New("empty plaintext")
	}

	return MsgType(plaintext[0]), plaintext[1:], nil
}

// Handshake performs the encrypted handshake.
// The caller MUST set a deadline on raw before calling (e.g., 5 seconds).
// initiator=true: we dialed, so we send sessionSalt + encrypted magic first.
// initiator=false: we accepted, so we read sessionSalt + encrypted magic first.
func Handshake(raw net.Conn, masterKey [32]byte, initiator bool) (*SecureConn, error) {
	if initiator {
		return handshakeInitiator(raw, masterKey)
	}
	return handshakeAcceptor(raw, masterKey)
}

func handshakeInitiator(raw net.Conn, masterKey [32]byte) (*SecureConn, error) {
	var sessionSalt [SessionSaltSize]byte
	if err := randomBytes(sessionSalt[:]); err != nil {
		return nil, fmt.Errorf("generate session salt: %w", err)
	}

	sessionKey := deriveSessionKey(masterKey, sessionSalt)
	aead, err := newAEAD(sessionKey)
	if err != nil {
		return nil, err
	}

	var nonce [NonceSize]byte
	if err := randomBytes(nonce[:]); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	encMagic := aead.Seal(nil, nonce[:], []byte(MagicBytes), nil)

	// Send: [sessionSalt 32][nonce 12][encrypted magic]
	if _, err := raw.Write(sessionSalt[:]); err != nil {
		return nil, fmt.Errorf("write session salt: %w", err)
	}
	if _, err := raw.Write(nonce[:]); err != nil {
		return nil, fmt.Errorf("write nonce: %w", err)
	}
	if _, err := raw.Write(encMagic); err != nil {
		return nil, fmt.Errorf("write encrypted magic: %w", err)
	}

	// Read response: [nonce 12][encrypted magic]
	// Response magic size = plaintext + AEAD overhead
	expectedEncSize := len(MagicBytes) + aead.Overhead()
	var respNonce [NonceSize]byte
	if _, err := io.ReadFull(raw, respNonce[:]); err != nil {
		return nil, fmt.Errorf("read response nonce: %w", err)
	}
	respEnc := make([]byte, expectedEncSize)
	if _, err := io.ReadFull(raw, respEnc); err != nil {
		return nil, fmt.Errorf("read response magic: %w", err)
	}

	respPlain, err := aead.Open(nil, respNonce[:], respEnc, nil)
	if err != nil {
		return nil, fmt.Errorf("handshake failed (wrong password?): %w", err)
	}
	if string(respPlain) != MagicBytes {
		return nil, errors.New("handshake failed: invalid magic")
	}

	return newSecureConn(raw, aead, true), nil
}

func handshakeAcceptor(raw net.Conn, masterKey [32]byte) (*SecureConn, error) {
	var sessionSalt [SessionSaltSize]byte
	if _, err := io.ReadFull(raw, sessionSalt[:]); err != nil {
		return nil, fmt.Errorf("read session salt: %w", err)
	}

	sessionKey := deriveSessionKey(masterKey, sessionSalt)
	aead, err := newAEAD(sessionKey)
	if err != nil {
		return nil, err
	}

	var nonce [NonceSize]byte
	if _, err := io.ReadFull(raw, nonce[:]); err != nil {
		return nil, fmt.Errorf("read nonce: %w", err)
	}
	expectedEncSize := len(MagicBytes) + aead.Overhead()
	encMagic := make([]byte, expectedEncSize)
	if _, err := io.ReadFull(raw, encMagic); err != nil {
		return nil, fmt.Errorf("read encrypted magic: %w", err)
	}

	plaintext, err := aead.Open(nil, nonce[:], encMagic, nil)
	if err != nil {
		return nil, fmt.Errorf("handshake failed (wrong password?): %w", err)
	}
	if string(plaintext) != MagicBytes {
		return nil, errors.New("handshake failed: invalid magic")
	}

	var respNonce [NonceSize]byte
	if err := randomBytes(respNonce[:]); err != nil {
		return nil, fmt.Errorf("generate response nonce: %w", err)
	}
	respEnc := aead.Seal(nil, respNonce[:], []byte(MagicBytes), nil)
	if _, err := raw.Write(respNonce[:]); err != nil {
		return nil, fmt.Errorf("write response nonce: %w", err)
	}
	if _, err := raw.Write(respEnc); err != nil {
		return nil, fmt.Errorf("write response magic: %w", err)
	}

	return newSecureConn(raw, aead, false), nil
}
