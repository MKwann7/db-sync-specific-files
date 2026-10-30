package dropbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func sha256sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

func hexEncode(b []byte) string {
	return hex.EncodeToString(b)
}

func TestContentHash_Empty(t *testing.T) {
	// Dropbox docs: "there is no block for an empty file... an empty string
	// would be formed", i.e. content_hash(empty) == sha256("") hex-encoded.
	got, err := ContentHash(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	const wantEmpty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got != wantEmpty {
		t.Errorf("empty file hash = %q, want %q", got, wantEmpty)
	}
}

func TestContentHash_SingleSmallBlock(t *testing.T) {
	data := []byte("hello dropbox")
	got, err := ContentHash(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Manually computed reference: sha256(sha256(data)) hex, since a file
	// smaller than 4MB is exactly one block.
	block := sha256sum(data)
	want := sha256sum(block)
	if got != hexEncode(want) {
		t.Errorf("hash = %q, want %q", got, hexEncode(want))
	}
}

func TestContentHash_MultiBlock(t *testing.T) {
	// Build a file spanning exactly two full 4MB blocks plus a partial
	// third block, and verify against a manual two/three-block computation.
	block1 := bytes.Repeat([]byte{0xAA}, contentHashBlockSize)
	block2 := bytes.Repeat([]byte{0xBB}, contentHashBlockSize)
	block3 := bytes.Repeat([]byte{0xCC}, 1000)

	full := append(append(append([]byte{}, block1...), block2...), block3...)

	got, err := ContentHash(bytes.NewReader(full))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	h1 := sha256sum(block1)
	h2 := sha256sum(block2)
	h3 := sha256sum(block3)
	concat := append(append(append([]byte{}, h1...), h2...), h3...)
	want := hexEncode(sha256sum(concat))

	if got != want {
		t.Errorf("hash = %q, want %q", got, want)
	}
	if strings.Contains(got, "0x") {
		t.Errorf("hash should be plain hex, got %q", got)
	}
}
