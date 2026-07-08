package transport

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// deriveKey computes master key from password using SHA-256.
// MVP: direct SHA-256. Phase 3: replace with scrypt.
func deriveKey(password string) [32]byte {
	return sha256.Sum256([]byte(password))
}

// deriveSessionKey derives a unique session key via HKDF-SHA256.
// sessionSalt is random 32 bytes generated per TCP connection.
// This prevents nonce reuse on reconnect: each connection gets a unique AES key.
func deriveSessionKey(masterKey [32]byte, sessionSalt [32]byte) [32]byte {
	// HKDF-Extract: PRK = HMAC-SHA256(salt, IKM)
	mac := hmac.New(sha256.New, sessionSalt[:])
	mac.Write(masterKey[:])
	prk := mac.Sum(nil)

	// HKDF-Expand: OKM = HMAC-SHA256(PRK, info || 0x01)
	info := []byte("teleport-session-v1")
	mac = hmac.New(sha256.New, prk)
	mac.Write(info)
	mac.Write([]byte{0x01})
	okm := mac.Sum(nil)

	var key [32]byte
	copy(key[:], okm)
	return key
}

// newAEAD creates AES-256-GCM from a session key.
func newAEAD(key [32]byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cipher.NewGCM: %w", err)
	}
	return aead, nil
}

// makeNonce builds a 12-byte nonce from a counter: [4 zero bytes][8-byte big-endian seq].
func makeNonce(seq uint64) [NonceSize]byte {
	var nonce [NonceSize]byte
	binary.BigEndian.PutUint64(nonce[4:], seq)
	return nonce
}

// randomBytes fills dst with cryptographically random bytes.
func randomBytes(dst []byte) error {
	_, err := rand.Read(dst)
	return err
}
