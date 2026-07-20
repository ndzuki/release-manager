package auth

import (
	"context"
	"log/slog"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// StartSessionCleanup periodically deletes expired persistent auth sessions.
// The goroutine exits when ctx is canceled.
func StartSessionCleanup(ctx context.Context, sessions store.AuthSessionStore, interval time.Duration, logger *slog.Logger) {
	if interval <= 0 {
		interval = time.Hour
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				deleted, err := sessions.DeleteExpired(ctx)
				if err != nil {
					logger.Error("delete expired auth sessions failed", "error", err)
					continue
				}
				if deleted > 0 {
					logger.Info("deleted expired auth sessions", "count", deleted)
				}
			}
		}
	}()
}
