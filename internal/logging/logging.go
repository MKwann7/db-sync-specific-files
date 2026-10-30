// Package logging configures the application's structured logger.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// Settings mirrors the logging section of the YAML configuration.
type Settings struct {
	Level  string
	Format string
}

// New builds a slog.Logger according to the given settings, writing to w.
// It never logs secret values; callers must ensure sensitive fields are
// omitted from log arguments.
func New(w io.Writer, s Settings) *slog.Logger {
	level := parseLevel(s.Level)
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	switch strings.ToLower(s.Format) {
	case "text", "console":
		handler = slog.NewTextHandler(w, opts)
	default:
		handler = slog.NewJSONHandler(w, opts)
	}
	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Redact returns a placeholder suitable for logging in place of a secret
// value such as a token, refresh token, or app secret. It never includes any
// part of the real value.
func Redact(_ string) string {
	return "[REDACTED]"
}
