package transport

const (
	// FrameHeaderSize is the length prefix size (uint32 big-endian).
	FrameHeaderSize = 4

	// MaxFrameSize is the maximum encrypted payload size (1 MB).
	MaxFrameSize = 1 << 20

	// NonceSize is the AES-GCM nonce size in bytes.
	NonceSize = 12

	// SessionSaltSize is the random salt size for HKDF session key derivation.
	SessionSaltSize = 32

	// MagicBytes is sent during handshake to verify the shared password. Contains protocol version.
	MagicBytes = "TELEPORT\x01"

	// FileChunkSize is the size of each file data chunk.
	FileChunkSize = 64 * 1024
)
