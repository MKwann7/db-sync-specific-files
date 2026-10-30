package filesystem

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWithinRoots_Allowed(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "sub", "file.txt")

	resolved, err := ResolveWithinRoots(target, []string{root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved == "" {
		t.Fatal("expected resolved path")
	}
}

func TestResolveWithinRoots_RejectsEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "file.txt")

	_, err := ResolveWithinRoots(target, []string{root})
	if !errors.Is(err, ErrOutsideAllowedRoots) {
		t.Fatalf("expected ErrOutsideAllowedRoots, got %v", err)
	}
}

func TestResolveWithinRoots_RejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported in this environment: %v", err)
	}
	target := filepath.Join(link, "file.txt")

	_, err := ResolveWithinRoots(target, []string{root})
	if !errors.Is(err, ErrOutsideAllowedRoots) {
		t.Fatalf("expected ErrOutsideAllowedRoots for symlink escape, got %v", err)
	}
}

func TestAtomicWriteFromReader_CreatesParentAndFile(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "nested", "out.txt")

	content := []byte("hello world")
	if err := AtomicWriteFromReader(dest, bytes.NewReader(content), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestAtomicWriteFromReader_NoPartialFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.txt")

	// Pre-existing destination must survive an interrupted write.
	if err := os.WriteFile(dest, []byte("original"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	failingReader := &errorReader{err: errors.New("simulated read failure")}
	err := AtomicWriteFromReader(dest, failingReader, 0o644)
	if err == nil {
		t.Fatal("expected an error from failing reader")
	}

	got, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatalf("destination file should be untouched: %v", readErr)
	}
	if string(got) != "original" {
		t.Errorf("destination content changed after failed write: %q", got)
	}

	// No leftover temp files.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "out.txt" {
			t.Errorf("unexpected leftover temp file: %s", e.Name())
		}
	}
}

type errorReader struct{ err error }

func (r *errorReader) Read(p []byte) (int, error) { return 0, r.err }

func TestUniqueConflictPath_NoCollision(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "manuscript.docx")

	calls := 0
	paths := map[string]bool{}
	for i := 0; i < 5; i++ {
		p, err := UniqueConflictPath(original,
			func() string { return "20240101T000000Z" },
			func() (string, error) {
				calls++
				return string(rune('a' + i)), nil
			},
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if paths[p] {
			t.Fatalf("duplicate conflict path generated: %s", p)
		}
		paths[p] = true
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("writing conflict file: %v", err)
		}
	}
}

func TestUniqueConflictPath_RetriesOnCollision(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "manuscript.docx")

	// Pre-create the first candidate so the function must retry.
	first := original[:len(original)-len(filepath.Ext(original))] + ".conflict-T-aaa.docx"
	if err := os.WriteFile(first, []byte("taken"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	attempts := 0
	p, err := UniqueConflictPath(original,
		func() string { return "T" },
		func() (string, error) {
			attempts++
			if attempts == 1 {
				return "aaa", nil
			}
			return "bbb", nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == first {
		t.Fatalf("expected a different path than the pre-existing collision, got %s", p)
	}
	if attempts < 2 {
		t.Fatalf("expected at least 2 attempts, got %d", attempts)
	}
}
