package filesystem

import (
	"fmt"
	"os"
)

// HashFile opens path and computes its content hash using the provided hash
// function (e.g. dropbox.ContentHash), without loading the whole file into
// memory at once (the hash function is expected to stream from the reader).
func HashFile(path string, hashFn func(r readerCloserLike) (string, error)) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening %q for hashing: %w", path, err)
	}
	defer f.Close()
	return hashFn(f)
}

// readerCloserLike exists only to document intent; os.File satisfies
// io.Reader directly, so callers can pass dropbox.ContentHash as hashFn.
type readerCloserLike = interface {
	Read(p []byte) (n int, err error)
}
