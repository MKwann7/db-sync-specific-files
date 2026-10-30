// Package auth implements Dropbox OAuth 2.0 authorization-code-with-offline
// access, plus automatic access token refresh for unattended operation.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultAuthorizeURL = "https://www.dropbox.com/oauth2/authorize"
	defaultTokenURL     = "https://api.dropboxapi.com/oauth2/token"

	// Scopes are the minimum Dropbox scopes required by dbsync.
	Scopes = "files.metadata.read files.content.read files.content.write"
)

// authorizeURL and tokenURL are package-level variables (rather than
// constants) solely so tests can redirect them to an httptest server
// without any real network access or credentials.
var (
	authorizeURL = defaultAuthorizeURL
	tokenURL     = defaultTokenURL
)

// Credentials are the Dropbox app credentials, sourced from environment
// variables. They are never logged.
type Credentials struct {
	AppKey    string
	AppSecret string
}

// TokenSet holds OAuth tokens. RefreshToken is empty on subsequent refresh
// responses (Dropbox only returns it on the initial code exchange).
type TokenSet struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// GenerateState returns a cryptographically random, URL-safe state value for
// CSRF protection during the OAuth authorization-code flow.
func GenerateState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating oauth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// AuthorizationURL builds the Dropbox OAuth 2.0 authorization URL for the
// authorization-code flow with offline access (refresh tokens).
func AuthorizationURL(creds Credentials, state, redirectURI string) string {
	q := url.Values{}
	q.Set("client_id", creds.AppKey)
	q.Set("response_type", "code")
	q.Set("token_access_type", "offline")
	q.Set("state", state)
	q.Set("force_reapprove", "false")
	if redirectURI != "" {
		q.Set("redirect_uri", redirectURI)
	}
	return authorizeURL + "?" + q.Encode()
}

// ExchangeCode exchanges an authorization code for an initial access +
// refresh token pair.
func ExchangeCode(ctx context.Context, creds Credentials, code, redirectURI string) (TokenSet, error) {
	form := url.Values{}
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", creds.AppKey)
	form.Set("client_secret", creds.AppSecret)
	if redirectURI != "" {
		form.Set("redirect_uri", redirectURI)
	}
	return doTokenRequest(ctx, form)
}

// RefreshAccessToken exchanges a long-lived refresh token for a new
// short-lived access token.
func RefreshAccessToken(ctx context.Context, creds Credentials, refreshToken string) (TokenSet, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", creds.AppKey)
	form.Set("client_secret", creds.AppSecret)
	return doTokenRequest(ctx, form)
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func doTokenRequest(ctx context.Context, form url.Values) (TokenSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenSet{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return TokenSet{}, fmt.Errorf("contacting dropbox oauth token endpoint: %w", err)
	}
	defer resp.Body.Close()

	var parsed tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return TokenSet{}, fmt.Errorf("decoding oauth token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if parsed.Error != "" {
			return TokenSet{}, &OAuthError{Code: parsed.Error, Description: parsed.ErrorDesc, StatusCode: resp.StatusCode}
		}
		return TokenSet{}, &OAuthError{Code: "unknown_error", Description: fmt.Sprintf("status %d", resp.StatusCode), StatusCode: resp.StatusCode}
	}

	ts := TokenSet{
		AccessToken:  parsed.AccessToken,
		RefreshToken: parsed.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(parsed.ExpiresIn) * time.Second),
	}
	return ts, nil
}

// OAuthError represents an error response from Dropbox's OAuth endpoints.
// It never carries token or secret values.
type OAuthError struct {
	Code        string
	Description string
	StatusCode  int
}

func (e *OAuthError) Error() string {
	return fmt.Sprintf("dropbox oauth error (%d) %s: %s", e.StatusCode, e.Code, e.Description)
}

// IsInvalidGrant reports whether the error indicates the refresh token was
// invalid, expired, or revoked and requires the user to re-run `auth login`.
func (e *OAuthError) IsInvalidGrant() bool {
	return e.Code == "invalid_grant"
}
