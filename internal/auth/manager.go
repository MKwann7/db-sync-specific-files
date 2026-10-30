package auth

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// refreshSkew is how long before actual expiry we proactively refresh, to
// avoid using a token that expires mid-request.
const refreshSkew = 2 * time.Minute

// Manager implements dropbox.TokenProvider, caching the current access
// token in memory and transparently refreshing it using the long-lived
// refresh token. It never persists the access token to disk and never logs
// token values.
type Manager struct {
	creds        Credentials
	refreshToken string
	logger       *slog.Logger

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

// NewManager creates a Manager for unattended operation using an existing
// refresh token (obtained via the `auth login` flow).
func NewManager(creds Credentials, refreshToken string, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{creds: creds, refreshToken: refreshToken, logger: logger}
}

// AccessToken returns a currently-valid access token, refreshing it first if
// it is missing or near expiry. Implements dropbox.TokenProvider.
func (m *Manager) AccessToken(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.accessToken != "" && time.Now().Before(m.expiresAt.Add(-refreshSkew)) {
		return m.accessToken, nil
	}

	if m.refreshToken == "" {
		return "", fmt.Errorf("no refresh token configured: run 'dbsync auth login' first")
	}

	m.logger.Debug("refreshing dropbox access token")
	ts, err := RefreshAccessToken(ctx, m.creds, m.refreshToken)
	if err != nil {
		if oauthErr, ok := err.(*OAuthError); ok && oauthErr.IsInvalidGrant() {
			m.logger.Error("dropbox refresh token is invalid, expired, or revoked; re-run 'dbsync auth login'")
			return "", fmt.Errorf("refresh token rejected by dropbox: %w", err)
		}
		return "", fmt.Errorf("refreshing dropbox access token: %w", err)
	}

	m.accessToken = ts.AccessToken
	m.expiresAt = ts.ExpiresAt
	m.logger.Debug("dropbox access token refreshed", "expires_in_seconds", int(time.Until(ts.ExpiresAt).Seconds()))
	return m.accessToken, nil
}
