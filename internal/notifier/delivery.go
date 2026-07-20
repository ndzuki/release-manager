package notifier

import (
	"context"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// Sender delivers a notification to its destination channel.
// Returns: error_code (stable), is4xx, error.
type Sender interface {
	Send(ctx context.Context, job *store.NotificationJob) (errorCode string, is4xx bool, err error)
}

// SecretResolver resolves secrets by reference without exposing secret values.
// It is intentionally minimal: callers pass a key/ref and receive the resolved
// value scoped to a single call. Secrets never appear in logs or last_error.
type SecretResolver interface {
	Resolve(ctx context.Context, key string) (string, error)
}

// Clock provides the current time, enabling test injection.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }
