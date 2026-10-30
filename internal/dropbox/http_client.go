package dropbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	// uploadSessionThreshold is the size above which uploads use chunked
	// upload sessions instead of the single-shot upload endpoint. Dropbox's
	// simple upload endpoint supports files up to 150MB; we chunk earlier
	// for safer memory/streaming behavior.
	uploadSessionThreshold = 150 * 1024 * 1024
	uploadChunkSize        = 8 * 1024 * 1024
)

// apiBaseURLForTest and contentBaseURLForTest are package-level variables
// (rather than constants) solely so tests can redirect them to an httptest
// server without any real network access or credentials.
var (
	apiBaseURLForTest     = "https://api.dropboxapi.com/2"
	contentBaseURLForTest = "https://content.dropboxapi.com/2"
)

// TokenProvider supplies a valid (auto-refreshed) Dropbox access token. It is
// implemented by internal/auth.Manager, kept as a narrow interface here to
// avoid an import cycle and to simplify testing.
type TokenProvider interface {
	AccessToken(ctx context.Context) (string, error)
}

// HTTPClient is the production Client implementation backed by the real
// Dropbox API v2 over HTTPS.
type HTTPClient struct {
	tokens TokenProvider
	http   *http.Client
	retry  RetryConfig
}

// NewHTTPClient constructs an HTTPClient. httpClient may be nil to use a
// default client with sane timeouts.
func NewHTTPClient(tokens TokenProvider, httpClient *http.Client) *HTTPClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &HTTPClient{tokens: tokens, http: httpClient, retry: DefaultRetryConfig}
}

func (c *HTTPClient) authHeader(ctx context.Context) (string, error) {
	tok, err := c.tokens.AccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("obtaining access token: %w", err)
	}
	return "Bearer " + tok, nil
}

// doRPC performs a JSON-in/JSON-out call to an api.dropboxapi.com endpoint.
func (c *HTTPClient) doRPC(ctx context.Context, path string, reqBody any, respBody any) error {
	return withRetry(ctx, c.retry, func() error {
		payload, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		auth, err := c.authHeader(ctx)
		if err != nil {
			return err
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBaseURLForTest+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Authorization", auth)
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := c.http.Do(httpReq)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		return decodeAPIResponse(resp, respBody)
	})
}

func decodeAPIResponse(resp *http.Response, out any) error {
	if resp.StatusCode == http.StatusOK {
		if out == nil {
			io.Copy(io.Discard, resp.Body)
			return nil
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	apiErr := &APIError{StatusCode: resp.StatusCode, Summary: summarizeError(body)}
	if resp.StatusCode == http.StatusUnauthorized {
		apiErr.Err = ErrUnauthorized
	} else if resp.StatusCode == http.StatusConflict {
		apiErr.Err = ErrConflict
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			apiErr.RetryAfter = secs
		}
	}
	return apiErr
}

// summarizeError extracts a short, secret-free human summary from a Dropbox
// error response body.
func summarizeError(body []byte) string {
	var parsed struct {
		ErrorSummary string `json:"error_summary"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.ErrorSummary != "" {
		return parsed.ErrorSummary
	}
	if len(body) > 200 {
		body = body[:200]
	}
	return string(body)
}

type dbxMetadata struct {
	Tag            string `json:".tag"`
	Name           string `json:"name"`
	PathLower      string `json:"path_lower"`
	ID             string `json:"id"`
	Rev            string `json:"rev"`
	ContentHash    string `json:"content_hash"`
	Size           int64  `json:"size"`
	ServerModified string `json:"server_modified"`
}

func (m dbxMetadata) toMetadata(path string) Metadata {
	md := Metadata{
		Path:        path,
		Name:        m.Name,
		Rev:         m.Rev,
		ContentHash: m.ContentHash,
		Size:        m.Size,
	}
	if m.ServerModified != "" {
		if t, err := time.Parse(time.RFC3339, m.ServerModified); err == nil {
			md.ServerModified = t
		}
	}
	if m.Tag == "deleted" {
		md.IsDeleted = true
	}
	return md
}

// GetMetadata implements Client.
func (c *HTTPClient) GetMetadata(ctx context.Context, path string) (Metadata, error) {
	var resp dbxMetadata
	err := c.doRPC(ctx, "/files/get_metadata", map[string]any{
		"path":                                path,
		"include_media_info":                  false,
		"include_deleted":                     false,
		"include_has_explicit_shared_members": false,
	}, &resp)
	if err != nil {
		var apiErr *APIError
		if asAPIError(err, &apiErr) && isPathNotFound(apiErr) {
			return Metadata{Path: path, IsDeleted: true}, nil
		}
		return Metadata{}, err
	}
	return resp.toMetadata(path), nil
}

func isPathNotFound(err *APIError) bool {
	return err.StatusCode == http.StatusConflict && containsAny(err.Summary, "not_found", "path/not_found")
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(s) >= len(sub) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

func asAPIError(err error, target **APIError) bool {
	if apiErr, ok := err.(*APIError); ok {
		*target = apiErr
		return true
	}
	return false
}

// Download implements Client.
func (c *HTTPClient) Download(ctx context.Context, path string) (Metadata, io.ReadCloser, error) {
	auth, err := c.authHeader(ctx)
	if err != nil {
		return Metadata{}, nil, err
	}
	argJSON, _ := json.Marshal(map[string]string{"path": path})

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, contentBaseURLForTest+"/files/download", nil)
	if err != nil {
		return Metadata{}, nil, err
	}
	httpReq.Header.Set("Authorization", auth)
	httpReq.Header.Set("Dropbox-API-Arg", string(argJSON))

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return Metadata{}, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		if err := decodeAPIResponse(resp, nil); err != nil {
			var apiErr *APIError
			if asAPIError(err, &apiErr) && isPathNotFound(apiErr) {
				return Metadata{Path: path, IsDeleted: true}, nil, ErrNotFound
			}
			return Metadata{}, nil, err
		}
	}

	var md dbxMetadata
	if result := resp.Header.Get("Dropbox-API-Result"); result != "" {
		_ = json.Unmarshal([]byte(result), &md)
	}
	return md.toMetadata(path), resp.Body, nil
}

// Upload implements Client, transparently choosing between the simple
// upload endpoint and chunked upload sessions based on size.
func (c *HTTPClient) Upload(ctx context.Context, req UploadRequest) (Metadata, error) {
	if req.Size > uploadSessionThreshold {
		return c.uploadLarge(ctx, req)
	}
	return c.uploadSmall(ctx, req)
}

func uploadModeArg(req UploadRequest) map[string]any {
	switch req.Mode {
	case UploadModeOverwrite:
		return map[string]any{".tag": "overwrite"}
	case UploadModeUpdate:
		return map[string]any{".tag": "update", "update": req.IfMatchRev}
	default:
		return map[string]any{".tag": "add"}
	}
}

func (c *HTTPClient) uploadSmall(ctx context.Context, req UploadRequest) (Metadata, error) {
	data, err := io.ReadAll(req.Reader)
	if err != nil {
		return Metadata{}, fmt.Errorf("reading upload content: %w", err)
	}

	var result Metadata
	err = withRetry(ctx, c.retry, func() error {
		auth, err := c.authHeader(ctx)
		if err != nil {
			return err
		}
		arg := map[string]any{
			"path":            req.RemotePath,
			"mode":            uploadModeArg(req),
			"autorename":      false,
			"mute":            true,
			"strict_conflict": req.Mode == UploadModeUpdate,
		}
		argJSON, _ := json.Marshal(arg)

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, contentBaseURLForTest+"/files/upload", bytes.NewReader(data))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Authorization", auth)
		httpReq.Header.Set("Content-Type", "application/octet-stream")
		httpReq.Header.Set("Dropbox-API-Arg", string(argJSON))

		resp, err := c.http.Do(httpReq)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		var md dbxMetadata
		if err := decodeAPIResponse(resp, &md); err != nil {
			return err
		}
		result = md.toMetadata(req.RemotePath)
		return nil
	})
	return result, err
}

type sessionStartResp struct {
	SessionID string `json:"session_id"`
}

func (c *HTTPClient) uploadLarge(ctx context.Context, req UploadRequest) (Metadata, error) {
	buf := make([]byte, uploadChunkSize)

	// Start session with the first chunk.
	n, err := io.ReadFull(req.Reader, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return Metadata{}, fmt.Errorf("reading first chunk: %w", err)
	}

	var start sessionStartResp
	if err := c.contentRPC(ctx, "/files/upload_session/start", map[string]any{"close": false}, buf[:n], &start); err != nil {
		return Metadata{}, fmt.Errorf("starting upload session: %w", err)
	}

	offset := int64(n)
	for {
		n, readErr := io.ReadFull(req.Reader, buf)
		if n == 0 && (readErr == io.EOF) {
			break
		}
		isLast := readErr == io.EOF || readErr == io.ErrUnexpectedEOF
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return Metadata{}, fmt.Errorf("reading chunk: %w", readErr)
		}

		if isLast {
			var md dbxMetadata
			cursor := map[string]any{"session_id": start.SessionID, "offset": offset}
			commit := map[string]any{
				"path":            req.RemotePath,
				"mode":            uploadModeArg(req),
				"autorename":      false,
				"mute":            true,
				"strict_conflict": req.Mode == UploadModeUpdate,
			}
			err := c.contentRPC(ctx, "/files/upload_session/finish", map[string]any{
				"cursor": cursor,
				"commit": commit,
			}, buf[:n], &md)
			if err != nil {
				return Metadata{}, fmt.Errorf("finishing upload session: %w", err)
			}
			return md.toMetadata(req.RemotePath), nil
		}

		cursor := map[string]any{"session_id": start.SessionID, "offset": offset}
		if err := c.contentRPC(ctx, "/files/upload_session/append_v2", map[string]any{"cursor": cursor, "close": false}, buf[:n], nil); err != nil {
			return Metadata{}, fmt.Errorf("appending upload session chunk: %w", err)
		}
		offset += int64(n)
	}

	// Degenerate case: file size was an exact multiple of chunk size and the
	// loop above already finished via isLast on the last full chunk, so we
	// should not reach here. Guard regardless.
	return Metadata{}, fmt.Errorf("upload session did not finish: unexpected end of stream")
}

// contentRPC performs a request to content.dropboxapi.com carrying a JSON
// Dropbox-API-Arg header and raw binary body, used for upload session calls.
func (c *HTTPClient) contentRPC(ctx context.Context, path string, arg map[string]any, body []byte, out any) error {
	return withRetry(ctx, c.retry, func() error {
		auth, err := c.authHeader(ctx)
		if err != nil {
			return err
		}
		argJSON, _ := json.Marshal(arg)

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, contentBaseURLForTest+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Authorization", auth)
		httpReq.Header.Set("Content-Type", "application/octet-stream")
		httpReq.Header.Set("Dropbox-API-Arg", string(argJSON))

		resp, err := c.http.Do(httpReq)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		return decodeAPIResponse(resp, out)
	})
}

// ListFolderLatestCursor implements Client.
func (c *HTTPClient) ListFolderLatestCursor(ctx context.Context, folderPath string, recursive bool) (string, error) {
	var resp struct {
		Cursor string `json:"cursor"`
	}
	err := c.doRPC(ctx, "/files/list_folder/get_latest_cursor", map[string]any{
		"path":      folderPath,
		"recursive": recursive,
	}, &resp)
	return resp.Cursor, err
}

// ListFolderContinue implements Client.
func (c *HTTPClient) ListFolderContinue(ctx context.Context, cursor string) ([]Metadata, string, bool, error) {
	var resp struct {
		Entries []dbxMetadata `json:"entries"`
		Cursor  string        `json:"cursor"`
		HasMore bool          `json:"has_more"`
	}
	err := c.doRPC(ctx, "/files/list_folder/continue", map[string]any{"cursor": cursor}, &resp)
	if err != nil {
		return nil, "", false, err
	}
	entries := make([]Metadata, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		entries = append(entries, e.toMetadata(e.PathLower))
	}
	return entries, resp.Cursor, resp.HasMore, nil
}
