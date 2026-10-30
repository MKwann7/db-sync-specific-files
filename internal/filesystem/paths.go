// Package filesystem provides safe local file operations: path resolution
// within permitted roots, atomic writes, and hashing helpers used by the
// synchronization engine.
package filesystem

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrOutsideAllowedRoots is returned when a resolved path escapes every
// configured allowed root (e.g. via a symlink).
var ErrOutsideAllowedRoots = errors.New("filesystem: path resolves outside permitted allowed_roots")

// ResolveWithinRoots resolves path (following symlinks on existing
// ancestors) to an absolute path and verifies it falls within one of roots.
// It returns the resolved path or ErrOutsideAllowedRoots.
func ResolveWithinRoots(path string, roots []string) (string, error) {
	resolved, err := ResolveSymlinkedAbs(path)
	if err != nil {
		return "", err
	}
	if len(roots) == 0 {
		return resolved, nil
	}
	for _, root := range roots {
		rroot, err := ResolveSymlinkedAbs(root)
		if err != nil {
			continue
		}
		if resolved == rroot || isWithin(rroot, resolved) {
			return resolved, nil
		}
	}
	return "", ErrOutsideAllowedRoots
}

// ResolveSymlinkedAbs returns the absolute, symlink-evaluated form of path.
// If the path (or some of its trailing components) does not yet exist, the
// existing ancestor prefix is resolved and the missing suffix re-appended
// unresolved, so callers can validate destinations that will be created.
func ResolveSymlinkedAbs(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)

	dir := abs
	var suffix []string
	for {
		real, err := filepath.EvalSymlinks(dir)
		if err == nil {
			full := real
			for i := len(suffix) - 1; i >= 0; i-- {
				full = filepath.Join(full, suffix[i])
			}
			return filepath.Clean(full), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs, nil
		}
		suffix = append(suffix, filepath.Base(dir))
		dir = parent
	}
}

func isWithin(root, child string) bool {
	sep := string(filepath.Separator)
	if root != sep && !hasTrailingSep(root) {
		root += sep
	}
	return len(child) > len(root) && child[:len(root)] == root
}

func hasTrailingSep(p string) bool {
	return len(p) > 0 && p[len(p)-1] == filepath.Separator
}

// AtomicWriteFromReader safely writes the content of r to destination path,
// using a temporary file in the same directory followed by an atomic
// rename, so that interrupted writes cannot corrupt the existing
// destination file. Parent directories are created as needed.
func AtomicWriteFromReader(destination string, r io.Reader, perm os.FileMode) (err error) {
	dir := filepath.Dir(destination)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating parent directory %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".dbsync-tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %q: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	if _, err = io.Copy(tmp, r); err != nil {
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("syncing temp file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err = os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("setting permissions on temp file: %w", err)
	}
	if err = os.Rename(tmpPath, destination); err != nil {
		return fmt.Errorf("renaming temp file into place: %w", err)
	}
	return nil
}

// UniqueConflictPath returns a collision-resistant path for preserving a
// "losing" file version during conflict resolution, of the form
// "<name>.conflict-<timestamp>-<rand><ext>". It verifies non-existence and
// retries with additional entropy on the rare chance of a collision,
// guaranteeing it never overwrites a prior conflict copy.
func UniqueConflictPath(original string, now func() string, randSuffix func() (string, error)) (string, error) {
	ext := filepath.Ext(original)
	base := original[:len(original)-len(ext)]

	for attempt := 0; attempt < 10; attempt++ {
		suffix, err := randSuffix()
		if err != nil {
			return "", err
		}
		candidate := fmt.Sprintf("%s.conflict-%s-%s%s", base, now(), suffix, ext)
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("unable to allocate a unique conflict filename for %q after multiple attempts", original)
}

// Exists reports whether path exists (following symlinks), distinguishing
// "does not exist" from other stat errors.
func Exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
