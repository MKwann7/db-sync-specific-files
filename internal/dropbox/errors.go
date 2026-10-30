package dropbox

import (
	"errors"
	"fmt"
)

// ErrNotFound indicates the requested path does not exist on Dropbox.
var ErrNotFound = errors.New("dropbox: path not found")

// ErrConflict indicates a conditional operation failed because the remote
// state did not match the expected revision (e.g. a conditional upload).
var ErrConflict = errors.New("dropbox: conflict")

// ErrUnauthorized indicates the access token is invalid, expired, or has
// been revoked.
var ErrUnauthorized = errors.New("dropbox: unauthorized")

// APIError wraps a Dropbox API error response with enough context for
// callers and logs without ever including request bodies that might carry
// credentials.
type APIError struct {
	StatusCode int
	Summary    string
	RetryAfter int // seconds, 0 if not specified
	Err        error
}

func (e *APIError) Error() string {
	return fmt.Sprintf("dropbox api error (status %d): %s", e.StatusCode, e.Summary)
}

func (e *APIError) Unwrap() error {
	return e.Err
}

// IsRetryable reports whether the error represents a transient condition
// (rate limiting or server error) worth retrying with backoff.
func (e *APIError) IsRetryable() bool {
	return e.StatusCode == 429 || e.StatusCode >= 500
}
