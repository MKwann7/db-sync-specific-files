package dropbox

import (
	"context"
	"io"
	"time"
)

// Metadata describes a Dropbox file's metadata relevant to synchronization
// decisions.
type Metadata struct {
	Path           string // lowercase path as returned by Dropbox (path_lower)
	Name           string
	Rev            string
	ContentHash    string
	Size           int64
	ServerModified time.Time
	IsDeleted      bool // true if the path does not currently exist on Dropbox
}

// UploadMode controls conflict behavior for uploads.
type UploadMode int

const (
	// UploadModeAdd fails if the destination already exists.
	UploadModeAdd UploadMode = iota
	// UploadModeOverwrite always replaces the destination.
	UploadModeOverwrite
	// UploadModeUpdate replaces the destination only if its current
	// revision matches IfMatchRev (optimistic concurrency / conditional
	// upload), failing otherwise.
	UploadModeUpdate
)

// UploadRequest describes a single-file upload operation.
type UploadRequest struct {
	RemotePath string
	Mode       UploadMode
	IfMatchRev string // required when Mode == UploadModeUpdate
	Reader     io.Reader
	Size       int64 // total size if known; enables upload-session chunking decisions
}

// Client is the narrow interface the synchronization engine depends on. The
// real implementation talks to the Dropbox API v2 over HTTPS; tests use an
// in-memory fake implementing the same interface, so the engine never needs
// network access to be exercised.
type Client interface {
	// GetMetadata fetches metadata for a single path. It returns a Metadata
	// with IsDeleted=true (and ErrNotFound-wrapped error returned as nil
	// error, IsDeleted true) when the path does not exist.
	GetMetadata(ctx context.Context, path string) (Metadata, error)

	// Download streams the content of path to the returned ReadCloser along
	// with its metadata. Callers must Close the reader.
	Download(ctx context.Context, path string) (Metadata, io.ReadCloser, error)

	// Upload uploads content described by req and returns the resulting
	// metadata. Implementations must use upload sessions transparently for
	// large files.
	Upload(ctx context.Context, req UploadRequest) (Metadata, error)

	// ListFolderLatestCursor returns a cursor representing the current
	// state of folderPath, for later use with ListFolderContinue.
	ListFolderLatestCursor(ctx context.Context, folderPath string, recursive bool) (string, error)

	// ListFolderContinue returns changed entries since cursor along with
	// the new cursor and whether more pages are available (hasMore=false
	// means the returned cursor is caught up to the present).
	ListFolderContinue(ctx context.Context, cursor string) (entries []Metadata, newCursor string, hasMore bool, err error)
}
