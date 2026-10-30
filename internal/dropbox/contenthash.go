package dropbox

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
)

// contentHashBlockSize is the Dropbox content-hash algorithm's fixed block
// size: https://www.dropbox.com/developers/reference/content-hash
const contentHashBlockSize = 4 * 1024 * 1024

// ContentHash computes the Dropbox "content_hash" value for the data read
// from r: the file is split into 4MB blocks, each block is SHA-256 hashed,
// the per-block hashes are concatenated, and the result is SHA-256 hashed
// again, with the final digest hex-encoded. This is NOT a plain SHA-256 of
// the file content.
func ContentHash(r io.Reader) (string, error) {
	overall := sha256.New()
	buf := make([]byte, contentHashBlockSize)
	for {
		n, readErr := io.ReadFull(r, buf)
		if n > 0 {
			block := sha256.Sum256(buf[:n])
			overall.Write(block[:])
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(overall.Sum(nil)), nil
}
