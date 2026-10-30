package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/MKwann7/db-sync-specific-files/internal/auth"
	"github.com/MKwann7/db-sync-specific-files/internal/config"
	"github.com/MKwann7/db-sync-specific-files/internal/dropbox"
	"github.com/MKwann7/db-sync-specific-files/internal/logging"
	"github.com/MKwann7/db-sync-specific-files/internal/state"
	syncpkg "github.com/MKwann7/db-sync-specific-files/internal/sync"
)

func configPath() string {
	if p := os.Getenv("DBSYNC_CONFIG"); p != "" {
		return p
	}
	return "/app/config.yaml"
}

func loadConfig() (*config.Config, error) {
	path := configPath()
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func newLogger(cfg *config.Config) *slog.Logger {
	return logging.New(os.Stderr, logging.Settings{Level: cfg.Logging.Level, Format: cfg.Logging.Format})
}

// loadCredentials reads Dropbox app credentials from the environment. The
// app secret and refresh token are never logged or echoed.
func loadCredentials() (auth.Credentials, error) {
	key := os.Getenv("DROPBOX_APP_KEY")
	secret := os.Getenv("DROPBOX_APP_SECRET")
	if key == "" || secret == "" {
		return auth.Credentials{}, fmt.Errorf("DROPBOX_APP_KEY and DROPBOX_APP_SECRET environment variables are required")
	}
	return auth.Credentials{AppKey: key, AppSecret: secret}, nil
}

func openState(cfg *config.Config) (*state.DB, error) {
	dir := filepath.Dir(cfg.StatePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating state directory %q: %w", dir, err)
	}
	return state.Open(cfg.StatePath)
}

// setup loads and validates configuration, resolves Dropbox credentials,
// and constructs the synchronization engine along with its logger and
// state database (which the caller must Close).
func setup() (*config.Config, *slog.Logger, *syncpkg.Engine, *state.DB, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	cfg.ConfigPath = configPath()
	if err := cfg.Validate(config.ValidateOptions{RequireAllowedRoots: true}); err != nil {
		return nil, nil, nil, nil, err
	}

	logger := newLogger(cfg)

	creds, err := loadCredentials()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	refreshToken := os.Getenv("DROPBOX_REFRESH_TOKEN")
	if refreshToken == "" {
		return nil, nil, nil, nil, fmt.Errorf("DROPBOX_REFRESH_TOKEN is not set; run 'dbsync auth login' first")
	}

	tokenMgr := auth.NewManager(creds, refreshToken, logger)
	client := dropbox.NewHTTPClient(tokenMgr, nil)

	db, err := openState(cfg)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	engine := syncpkg.New(cfg, client, db, logger)
	return cfg, logger, engine, db, nil
}
