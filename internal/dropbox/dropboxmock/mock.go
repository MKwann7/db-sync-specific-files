package dropboxmock

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/MKwann7/db-sync-specific-files/internal/dropbox"
)

// Client is an in-memory fake implementing dropbox.Client, suitable for
// deterministic tests that must not reach the network.
type Client struct {
	mu     sync.Mutex
	files  map[string]*entry
	revSeq int
	// Err, if set, is returned by every method call (used to simulate API
	// failures / rate limiting in tests).
	Err error
}

type entry struct {
	content     []byte
	rev         string
	contentHash string
	modified    time.Time
}

// NewClient creates an empty mock Dropbox filesystem.
func NewClient() *Client {
	return &Client{files: make(map[string]*entry)}
}

// Seed directly installs a file at path with the given content, bypassing
// Upload, for test setup convenience.
func (c *Client) Seed(path string, content []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revSeq++
	h, _ := dropbox.ContentHash(byteReader(content))
	c.files[path] = &entry{
		content:     content,
		rev:         fmt.Sprintf("rev%d", c.revSeq),
		contentHash: h,
		modified:    time.Now(),
	}
}

// Get returns the raw content currently stored at path, for assertions.
func (c *Client) Get(path string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.files[path]
	if !ok {
		return nil, false
	}
	return append([]byte{}, e.content...), true
}

func (c *Client) GetMetadata(ctx context.Context, path string) (dropbox.Metadata, error) {
	if c.Err != nil {
		return dropbox.Metadata{}, c.Err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.files[path]
	if !ok {
		return dropbox.Metadata{Path: path, IsDeleted: true}, nil
	}
	return dropbox.Metadata{
		Path:           path,
		Name:           path,
		Rev:            e.rev,
		ContentHash:    e.contentHash,
		Size:           int64(len(e.content)),
		ServerModified: e.modified,
	}, nil
}

func (c *Client) Download(ctx context.Context, path string) (dropbox.Metadata, io.ReadCloser, error) {
	if c.Err != nil {
		return dropbox.Metadata{}, nil, c.Err
	}
	md, err := c.GetMetadata(ctx, path)
	if err != nil {
		return dropbox.Metadata{}, nil, err
	}
	if md.IsDeleted {
		return md, nil, dropbox.ErrNotFound
	}
	c.mu.Lock()
	e := c.files[path]
	c.mu.Unlock()
	return md, io.NopCloser(byteReader(e.content)), nil
}

func (c *Client) Upload(ctx context.Context, req dropbox.UploadRequest) (dropbox.Metadata, error) {
	if c.Err != nil {
		return dropbox.Metadata{}, c.Err
	}
	data, err := io.ReadAll(req.Reader)
	if err != nil {
		return dropbox.Metadata{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	existing, exists := c.files[req.RemotePath]
	switch req.Mode {
	case dropbox.UploadModeAdd:
		if exists {
			return dropbox.Metadata{}, &dropbox.APIError{StatusCode: 409, Summary: "path/conflict/file", Err: dropbox.ErrConflict}
		}
	case dropbox.UploadModeUpdate:
		if !exists {
			return dropbox.Metadata{}, &dropbox.APIError{StatusCode: 409, Summary: "path/not_found", Err: dropbox.ErrConflict}
		}
		if existing.rev != req.IfMatchRev {
			return dropbox.Metadata{}, &dropbox.APIError{StatusCode: 409, Summary: "path/conflict/update_rev_mismatch", Err: dropbox.ErrConflict}
		}
	case dropbox.UploadModeOverwrite:
		// always allowed
	}

	c.revSeq++
	h, _ := dropbox.ContentHash(byteReader(data))
	e := &entry{content: data, rev: fmt.Sprintf("rev%d", c.revSeq), contentHash: h, modified: time.Now()}
	c.files[req.RemotePath] = e

	return dropbox.Metadata{
		Path:           req.RemotePath,
		Name:           req.RemotePath,
		Rev:            e.rev,
		ContentHash:    e.contentHash,
		Size:           int64(len(e.content)),
		ServerModified: e.modified,
	}, nil
}

func (c *Client) ListFolderLatestCursor(ctx context.Context, folderPath string, recursive bool) (string, error) {
	return "cursor-0", c.Err
}

func (c *Client) ListFolderContinue(ctx context.Context, cursor string) ([]dropbox.Metadata, string, bool, error) {
	if c.Err != nil {
		return nil, "", false, c.Err
	}
	return nil, cursor, false, nil
}

func byteReader(b []byte) *bytesReader {
	return &bytesReader{data: b}
}

// bytesReader is a tiny io.Reader over a byte slice, equivalent to
// bytes.NewReader but kept local to avoid an extra import at call sites.
type bytesReader struct {
	data []byte
	pos  int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
