package clipboard

import (
	"crypto/sha256"
	"sort"
)

// HashText returns SHA-256 of a UTF-8 string.
func HashText(s string) [32]byte {
	return sha256.Sum256([]byte(s))
}

// HashFiles returns SHA-256 of file paths sorted lexicographically and joined with newlines.
// Sorting ensures deterministic hashes regardless of OS file ordering.
func HashFiles(files []FileMeta) [32]byte {
	// Sort by LocalPath to ensure deterministic hash
	sorted := make([]FileMeta, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].LocalPath < sorted[j].LocalPath
	})

	h := sha256.New()
	for _, f := range sorted {
		h.Write([]byte(f.LocalPath))
		h.Write([]byte{'\n'})
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}
