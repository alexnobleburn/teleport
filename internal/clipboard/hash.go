package clipboard

import "crypto/sha256"

// HashText returns SHA-256 of a UTF-8 string.
func HashText(s string) [32]byte {
	return sha256.Sum256([]byte(s))
}

// HashFiles returns SHA-256 of sorted file paths concatenated with newlines.
func HashFiles(files []FileMeta) [32]byte {
	h := sha256.New()
	for _, f := range files {
		h.Write([]byte(f.LocalPath))
		h.Write([]byte{'\n'})
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}
