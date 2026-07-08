package transport

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
)

func TestDeriveKey_Deterministic(t *testing.T) {
	k1 := deriveKey("password123")
	k2 := deriveKey("password123")
	if k1 != k2 {
		t.Fatal("same password should produce same key")
	}
}

func TestDeriveKey_Different(t *testing.T) {
	k1 := deriveKey("password1")
	k2 := deriveKey("password2")
	if k1 == k2 {
		t.Fatal("different passwords should produce different keys")
	}
}

func TestDeriveSessionKey_Deterministic(t *testing.T) {
	mk := deriveKey("pass")
	var salt [32]byte
	copy(salt[:], "fixed-salt-for-test-1234567890ab")
	sk1 := deriveSessionKey(mk, salt)
	sk2 := deriveSessionKey(mk, salt)
	if sk1 != sk2 {
		t.Fatal("same inputs should produce same session key")
	}
}

func TestDeriveSessionKey_DifferentSalt(t *testing.T) {
	mk := deriveKey("pass")
	var salt1, salt2 [32]byte
	copy(salt1[:], "salt-aaaaaaaaaaaaaaaaaaaaaaaaaaa")
	copy(salt2[:], "salt-bbbbbbbbbbbbbbbbbbbbbbbbbbb")
	sk1 := deriveSessionKey(mk, salt1)
	sk2 := deriveSessionKey(mk, salt2)
	if sk1 == sk2 {
		t.Fatal("different salts should produce different session keys")
	}
}

func TestHandshake_Success(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	mk := deriveKey("shared-password")

	errCh := make(chan error, 2)
	var sc1, sc2 *SecureConn

	go func() {
		var err error
		sc1, err = Handshake(c1, mk, true) // initiator
		errCh <- err
	}()
	go func() {
		var err error
		sc2, err = Handshake(c2, mk, false) // acceptor
		errCh <- err
	}()

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("handshake error: %v", err)
		}
	}
	if sc1 == nil || sc2 == nil {
		t.Fatal("secure connections should not be nil")
	}
}

func TestHandshake_WrongPassword(t *testing.T) {
	c1, c2 := net.Pipe()

	mk1 := deriveKey("password-A")
	mk2 := deriveKey("password-B")

	errCh := make(chan error, 2)

	go func() {
		_, err := Handshake(c1, mk1, true)
		c1.Close() // unblock the other side
		errCh <- err
	}()
	go func() {
		_, err := Handshake(c2, mk2, false)
		c2.Close() // unblock the other side
		errCh <- err
	}()

	// At least one side should fail
	var errs []error
	for i := 0; i < 2; i++ {
		errs = append(errs, <-errCh)
	}
	anyErr := errs[0] != nil || errs[1] != nil
	if !anyErr {
		t.Fatal("handshake with wrong password should fail")
	}
}

func setupPair(t *testing.T) (*SecureConn, *SecureConn) {
	t.Helper()

	// Use real TCP so reads/writes don't block synchronously like net.Pipe
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	mk := deriveKey("test")
	type result struct {
		sc  *SecureConn
		err error
	}

	acceptCh := make(chan result, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			acceptCh <- result{nil, err}
			return
		}
		sc, err := Handshake(raw, mk, false)
		acceptCh <- result{sc, err}
	}()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	sc1, err := Handshake(raw, mk, true)
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}

	r := <-acceptCh
	if r.err != nil {
		sc1.raw.Close()
		t.Fatal(r.err)
	}

	t.Cleanup(func() { sc1.raw.Close(); r.sc.raw.Close() })
	return sc1, r.sc
}

func TestSendReceiveText(t *testing.T) {
	sc1, sc2 := setupPair(t)
	logger := slog.Default()

	sender := NewSender(sc1, logger)
	defer sender.Close()

	tests := []string{
		"hello",
		"Привет мир",
		"emoji: 🌍🚀",
		"multi\nline\ntext",
		"",
	}

	for _, text := range tests {
		if err := sender.SendText(text); err != nil {
			t.Fatalf("SendText(%q): %v", text, err)
		}

		msgType, payload, err := sc2.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		if msgType != MsgText {
			t.Fatalf("expected MsgText, got 0x%02x", msgType)
		}
		if string(payload) != text {
			t.Fatalf("expected %q, got %q", text, string(payload))
		}
	}
}

func TestSendReceiveText_Large(t *testing.T) {
	sc1, sc2 := setupPair(t)
	logger := slog.Default()

	sender := NewSender(sc1, logger)
	defer sender.Close()

	// 100KB text
	text := strings.Repeat("A", 100*1024)
	if err := sender.SendText(text); err != nil {
		t.Fatalf("SendText large: %v", err)
	}

	msgType, payload, err := sc2.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if msgType != MsgText {
		t.Fatalf("expected MsgText, got 0x%02x", msgType)
	}
	if len(payload) != len(text) {
		t.Fatalf("expected %d bytes, got %d", len(text), len(payload))
	}
}

func TestNonceUniqueness(t *testing.T) {
	seen := make(map[[NonceSize]byte]bool)
	for i := uint64(0); i < 10000; i++ {
		n := makeNonce(i)
		if seen[n] {
			t.Fatalf("duplicate nonce at seq %d", i)
		}
		seen[n] = true
	}
}

func TestSendReceiveFile(t *testing.T) {
	sc1, sc2 := setupPair(t)
	logger := slog.Default()

	sender := NewSender(sc1, logger)
	defer sender.Close()

	content := []byte("file content here 123")
	var checksum [32]byte
	copy(checksum[:], "fake-checksum-32-bytes-padding!!")

	if err := sender.SendFile("test.txt", int64(len(content)), checksum, bytes.NewReader(content)); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	// Read FileHeader
	msgType, _, err := sc2.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame header: %v", err)
	}
	if msgType != MsgFileHeader {
		t.Fatalf("expected MsgFileHeader, got 0x%02x", msgType)
	}

	// Read chunks until FileDone
	var received []byte
	for {
		msgType, payload, err := sc2.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame chunk: %v", err)
		}
		if msgType == MsgFileDone {
			break
		}
		if msgType != MsgFileChunk {
			t.Fatalf("expected MsgFileChunk or MsgFileDone, got 0x%02x", msgType)
		}
		received = append(received, payload...)
	}
	if !bytes.Equal(received, content) {
		t.Fatalf("file content mismatch: got %q", received)
	}
}

func TestSendReceiveBatch(t *testing.T) {
	sc1, sc2 := setupPair(t)
	logger := slog.Default()

	sender := NewSender(sc1, logger)
	defer sender.Close()

	var cs [32]byte
	files := []FileToSend{
		{Name: "a.txt", Size: 3, Checksum: cs, Reader: strings.NewReader("aaa")},
		{Name: "b.txt", Size: 3, Checksum: cs, Reader: strings.NewReader("bbb")},
	}

	if err := sender.SendFiles(files); err != nil {
		t.Fatalf("SendFiles: %v", err)
	}

	// Expect: BatchBegin, FileHeader, FileChunk(s), FileDone, FileHeader, FileChunk(s), FileDone, BatchEnd
	msgType, _, err := sc2.ReadFrame()
	if err != nil || msgType != MsgBatchBegin {
		t.Fatalf("expected MsgBatchBegin, got 0x%02x, err=%v", msgType, err)
	}

	for i := 0; i < 2; i++ {
		msgType, _, err = sc2.ReadFrame()
		if err != nil || msgType != MsgFileHeader {
			t.Fatalf("file %d: expected MsgFileHeader, got 0x%02x", i, msgType)
		}
		// Read chunks until Done
		for {
			msgType, _, err = sc2.ReadFrame()
			if err != nil {
				t.Fatal(err)
			}
			if msgType == MsgFileDone {
				break
			}
		}
	}

	msgType, _, err = sc2.ReadFrame()
	if err != nil || msgType != MsgBatchEnd {
		t.Fatalf("expected MsgBatchEnd, got 0x%02x, err=%v", msgType, err)
	}
}

// Ensure Listener and Dial work together
func TestListenerDial(t *testing.T) {
	logger := slog.Default()
	password := "integration-test"

	lst, err := NewListener(0, password, logger) // port 0 = random
	if err != nil {
		t.Fatal(err)
	}
	defer lst.Close()

	received := make(chan string, 1)
	handler := &testHandler{textCh: received}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go lst.Accept(ctx, func() ReceiveHandler { return handler })

	// Dial
	sender, err := Dial(lst.Addr(), password, logger)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sender.Close()

	if err := sender.SendText("hello from dialer"); err != nil {
		t.Fatalf("SendText: %v", err)
	}

	text := <-received
	if text != "hello from dialer" {
		t.Fatalf("expected 'hello from dialer', got %q", text)
	}
}

type testHandler struct {
	textCh chan string
}

func (h *testHandler) OnText(text string)                                          { h.textCh <- text }
func (h *testHandler) OnFile(name string, size int64, checksum [32]byte, r io.Reader) { io.Copy(io.Discard, r) }
func (h *testHandler) OnBatchBegin(count int)                                       {}
func (h *testHandler) OnBatchEnd()                                                  {}
