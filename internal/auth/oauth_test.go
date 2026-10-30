package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGenerateState_IsUniqueAndNonEmpty(t *testing.T) {
	s1, err := GenerateState()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s2, err := GenerateState()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s1 == "" || s2 == "" {
		t.Fatal("expected non-empty state values")
	}
	if s1 == s2 {
		t.Fatal("expected different state values across calls (CSRF protection)")
	}
}

func TestAuthorizationURL_IncludesOfflineAccessAndState(t *testing.T) {
	creds := Credentials{AppKey: "test-app-key"}
	authURL := AuthorizationURL(creds, "my-state", "http://localhost:53682/callback")

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("invalid URL: %v", err)
	}
	q := u.Query()
	if q.Get("client_id") != "test-app-key" {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("token_access_type") != "offline" {
		t.Errorf("token_access_type = %q, want offline (required for refresh tokens)", q.Get("token_access_type"))
	}
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q", q.Get("response_type"))
	}
	if q.Get("state") != "my-state" {
		t.Errorf("state = %q", q.Get("state"))
	}
	if q.Get("redirect_uri") != "http://localhost:53682/callback" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	// The app secret must never appear in the authorization URL.
	if strings.Contains(authURL, "secret") {
		t.Error("authorization URL should never contain the app secret")
	}
}

// withMockTokenServer points the package-level tokenURL at an httptest
// server for the duration of the test, restoring it afterwards.
func withMockTokenServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	orig := tokenURL
	tokenURL = srv.URL
	t.Cleanup(func() { tokenURL = orig })
}

func TestExchangeCode_Success(t *testing.T) {
	withMockTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing form: %v", err)
		}
		if r.FormValue("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q", r.FormValue("grant_type"))
		}
		if r.FormValue("code") != "auth-code-123" {
			t.Errorf("code = %q", r.FormValue("code"))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-xyz",
			"refresh_token": "refresh-abc",
			"expires_in":    14400,
			"token_type":    "bearer",
		})
	})

	ts, err := ExchangeCode(context.Background(), Credentials{AppKey: "k", AppSecret: "s"}, "auth-code-123", "http://localhost/callback")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts.AccessToken != "access-xyz" || ts.RefreshToken != "refresh-abc" {
		t.Errorf("unexpected token set: %+v", ts)
	}
	if time.Until(ts.ExpiresAt) < time.Hour {
		t.Errorf("expected expiry roughly 4 hours out, got %v", ts.ExpiresAt)
	}
}

func TestRefreshAccessToken_Success_NoNewRefreshToken(t *testing.T) {
	withMockTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.FormValue("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %q", r.FormValue("grant_type"))
		}
		if r.FormValue("refresh_token") != "existing-refresh-token" {
			t.Errorf("refresh_token = %q", r.FormValue("refresh_token"))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access-token",
			"expires_in":   14400,
		})
	})

	ts, err := RefreshAccessToken(context.Background(), Credentials{AppKey: "k", AppSecret: "s"}, "existing-refresh-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts.AccessToken != "new-access-token" {
		t.Errorf("access token = %q", ts.AccessToken)
	}
}

func TestRefreshAccessToken_InvalidGrant(t *testing.T) {
	withMockTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"error":             "invalid_grant",
			"error_description": "refresh token is invalid or revoked",
		})
	})

	_, err := RefreshAccessToken(context.Background(), Credentials{AppKey: "k", AppSecret: "s"}, "revoked-token")
	if err == nil {
		t.Fatal("expected an error")
	}
	oauthErr, ok := err.(*OAuthError)
	if !ok {
		t.Fatalf("expected *OAuthError, got %T", err)
	}
	if !oauthErr.IsInvalidGrant() {
		t.Errorf("expected IsInvalidGrant() true, got code=%q", oauthErr.Code)
	}
}

func TestManager_CachesTokenAndDoesNotRefreshUnnecessarily(t *testing.T) {
	refreshCount := 0
	withMockTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		refreshCount++
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "token-call-" + strconv.Itoa(refreshCount),
			"expires_in":   3600,
		})
	})

	mgr := NewManager(Credentials{AppKey: "k", AppSecret: "s"}, "refresh-token", nil)

	tok1, err := mgr.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tok2, err := mgr.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok1 != tok2 {
		t.Errorf("expected cached token to be reused: %q vs %q", tok1, tok2)
	}
	if refreshCount != 1 {
		t.Errorf("expected exactly 1 refresh call for two cache-hit requests, got %d", refreshCount)
	}
}

func TestManager_RefreshesAgainAfterExpiry(t *testing.T) {
	refreshCount := 0
	withMockTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		refreshCount++
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "token-call-" + strconv.Itoa(refreshCount),
			"expires_in":   1, // expires almost immediately, well within refreshSkew
		})
	})

	mgr := NewManager(Credentials{AppKey: "k", AppSecret: "s"}, "refresh-token", nil)
	tok1, err := mgr.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tok2, err := mgr.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok1 == tok2 {
		t.Error("expected a fresh token to be fetched once the cached one is within the refresh skew window")
	}
	if refreshCount < 2 {
		t.Errorf("expected at least 2 refresh calls, got %d", refreshCount)
	}
}

func TestManager_NoRefreshTokenConfigured(t *testing.T) {
	mgr := NewManager(Credentials{AppKey: "k", AppSecret: "s"}, "", nil)
	_, err := mgr.AccessToken(context.Background())
	if err == nil {
		t.Fatal("expected an error when no refresh token is configured")
	}
	if !strings.Contains(err.Error(), "auth login") {
		t.Errorf("expected actionable guidance in error, got: %v", err)
	}
}
