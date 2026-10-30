package filesystem

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// AtomicWriteVerified streams the content of r into a temporary file (never
// buffering the whole file in memory), then re-reads that temp file to
// compute its hash with hashFn and compares it against wantHash (when
// non-empty). Only on a successful match is the temp file atomically
// renamed into place at destination. On any failure -- including a hash
// mismatch -- the existing destination file is left completely untouched
// and the temporary file is removed, so interrupted or corrupted downloads
// can never clobber good data.
func AtomicWriteVerified(destination string, r io.Reader, wantHash string, hashFn func(io.Reader) (string, error), perm os.FileMode) (gotHash string, err error) {
	dir := filepath.Dir(destination)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating parent directory %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".dbsync-tmp-*")
	if err != nil {
		return "", fmt.Errorf("creating temp file in %q: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmp, r); err != nil {
		return "", fmt.Errorf("writing temp file: %w", err)
	}

	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seeking temp file for verification: %w", err)
	}
	computedHash, err := hashFn(tmp)
	if err != nil {
		return "", fmt.Errorf("hashing downloaded content: %w", err)
	}
	if wantHash != "" && computedHash != wantHash {
		return computedHash, fmt.Errorf("downloaded content hash %q does not match expected %q; aborting to avoid corrupting destination", computedHash, wantHash)
	}

	if err := tmp.Sync(); err != nil {
		return computedHash, fmt.Errorf("syncing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return computedHash, fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return computedHash, fmt.Errorf("setting permissions on temp file: %w", err)
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		return computedHash, fmt.Errorf("renaming temp file into place: %w", err)
	}
	cleanup = false
	return computedHash, nil
}
