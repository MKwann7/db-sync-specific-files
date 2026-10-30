package dropbox

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type staticTokenProvider struct{ token string }

func (s staticTokenProvider) AccessToken(ctx context.Context) (string, error) {
	return s.token, nil
}

func newTestHTTPClient(t *testing.T, apiHandler, contentHandler http.HandlerFunc) *HTTPClient {
	t.Helper()
	mux := http.NewServeMux()
	if apiHandler != nil {
		mux.HandleFunc("/api/", apiHandler)
	}
	if contentHandler != nil {
		mux.HandleFunc("/content/", contentHandler)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	origAPI, origContent := apiBaseURLForTest, contentBaseURLForTest
	apiBaseURLForTest = srv.URL + "/api"
	contentBaseURLForTest = srv.URL + "/content"
	t.Cleanup(func() {
		apiBaseURLForTest = origAPI
		contentBaseURLForTest = origContent
	})

	c := NewHTTPClient(staticTokenProvider{"tok"}, srv.Client())
	c.retry = RetryConfig{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 5 * time.Millisecond}
	return c
}

func TestHTTPClient_GetMetadata_NotFound(t *testing.T) {
	client := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{"error_summary": "path/not_found/..."})
	}, nil)

	md, err := client.GetMetadata(context.Background(), "/missing.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !md.IsDeleted {
		t.Error("expected IsDeleted=true for a not-found path")
	}
}

func TestHTTPClient_RetriesOnRateLimit_ThenSucceeds(t *testing.T) {
	var calls int32
	client := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]any{"error_summary": "too_many_requests/..."})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"name": "file.txt", "path_lower": "/file.txt", "rev": "r1",
			"content_hash": "abc", "size": 3,
		})
	}, nil)

	md, err := client.GetMetadata(context.Background(), "/file.txt")
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if md.Rev != "r1" {
		t.Errorf("rev = %q", md.Rev)
	}
	if calls != 2 {
		t.Errorf("expected exactly 2 calls (1 failure + 1 success), got %d", calls)
	}
}

func TestHTTPClient_GivesUpAfterMaxRetries(t *testing.T) {
	var calls int32
	client := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{"error_summary": "internal_server_error/..."})
	}, nil)

	_, err := client.GetMetadata(context.Background(), "/file.txt")
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls != 3 {
		t.Errorf("expected exactly MaxAttempts=3 calls, got %d", calls)
	}
}

func TestHTTPClient_UnauthorizedIsNotRetried(t *testing.T) {
	var calls int32
	client := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{"error_summary": "invalid_access_token/..."})
	}, nil)

	_, err := client.GetMetadata(context.Background(), "/file.txt")
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call (401 must not be retried), got %d", calls)
	}
}

func TestHTTPClient_UploadAndDownload_RoundTrip(t *testing.T) {
	stored := map[string][]byte{}

	client := newTestHTTPClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
		arg := r.Header.Get("Dropbox-API-Arg")
		switch {
		case strings.Contains(r.URL.Path, "upload"):
			body := make([]byte, r.ContentLength)
			r.Body.Read(body)
			stored["/up.txt"] = body
			json.NewEncoder(w).Encode(map[string]any{
				"name": "up.txt", "path_lower": "/up.txt", "rev": "rev1", "content_hash": "h1", "size": len(body),
			})
		case strings.Contains(r.URL.Path, "download"):
			_ = arg
			data := stored["/up.txt"]
			w.Header().Set("Dropbox-API-Result", `{"name":"up.txt","path_lower":"/up.txt","rev":"rev1","content_hash":"h1","size":`+strconv.Itoa(len(data))+`}`)
			w.Write(data)
		}
	})

	content := []byte("round trip content")
	md, err := client.Upload(context.Background(), UploadRequest{
		RemotePath: "/up.txt", Mode: UploadModeAdd, Reader: byteSliceReader(content), Size: int64(len(content)),
	})
	if err != nil {
		t.Fatalf("upload error: %v", err)
	}
	if md.Rev != "rev1" {
		t.Errorf("rev = %q", md.Rev)
	}

	_, body, err := client.Download(context.Background(), "/up.txt")
	if err != nil {
		t.Fatalf("download error: %v", err)
	}
	defer body.Close()
	gotBuf := make([]byte, len(content))
	n, _ := body.Read(gotBuf)
	if string(gotBuf[:n]) != string(content) {
		t.Errorf("downloaded content = %q, want %q", gotBuf[:n], content)
	}
}

type byteSliceReaderT struct {
	data []byte
	pos  int
}

func (r *byteSliceReaderT) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func byteSliceReader(b []byte) *byteSliceReaderT { return &byteSliceReaderT{data: b} }
