package dropbox

import (
	"context"
	"math/rand"
	"time"
)

// RetryConfig controls exponential backoff with jitter for retryable
// requests.
type RetryConfig struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// DefaultRetryConfig is a sensible default: up to 5 attempts, starting at
// 500ms and capping at 30s, with full jitter.
var DefaultRetryConfig = RetryConfig{
	MaxAttempts: 5,
	BaseDelay:   500 * time.Millisecond,
	MaxDelay:    30 * time.Second,
}

// withRetry executes fn, retrying on retryable errors with exponential
// backoff and jitter. If an *APIError carries a RetryAfter hint (e.g. from a
// 429 response), that delay is honored instead of the computed backoff. The
// context is respected for cancellation between attempts.
func withRetry(ctx context.Context, cfg RetryConfig, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err

		apiErr, ok := err.(*APIError)
		if !ok || !apiErr.IsRetryable() {
			return err
		}
		if attempt == cfg.MaxAttempts-1 {
			break
		}

		delay := backoffDelay(cfg, attempt)
		if apiErr.RetryAfter > 0 {
			delay = time.Duration(apiErr.RetryAfter) * time.Second
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func backoffDelay(cfg RetryConfig, attempt int) time.Duration {
	delay := cfg.BaseDelay * time.Duration(1<<uint(attempt))
	if delay > cfg.MaxDelay {
		delay = cfg.MaxDelay
	}
	// Full jitter: random value in [0, delay].
	jittered := time.Duration(rand.Int63n(int64(delay) + 1))
	return jittered
}
