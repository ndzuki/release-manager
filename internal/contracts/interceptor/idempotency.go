package interceptor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/ndzuki/release-manager/internal/contracts"
	"github.com/ndzuki/release-manager/internal/store"
)

// IdempotencyStore is the subset of store.IdempotencyStore needed by the interceptor.
type IdempotencyStore interface {
	CreateOrGet(ctx context.Context, record *store.IdempotencyRecord) (*store.IdempotencyRecord, bool, error)
}

// NewIdempotencyInterceptor returns a Connect unary interceptor that enforces
// idempotency via the Idempotency-Key header. Scope is built from the caller
// identity and RPC procedure to prevent cross-tenant key reuse.
//
// On the first call with a given scope+key:
//   - Computes SHA-256 of the serialized request payload as request_hash.
//   - Creates an idempotency record and lets the handler execute.
//
// On idempotent replay (same scope+key+hash):
//   - Allows the handler to execute (idempotent by design).
//   - Returns the same result without creating a duplicate record.
//
// On conflict (same scope+key, different hash):
//   - Returns CodeAlreadyExists immediately.
func NewIdempotencyInterceptor(
	idemStore IdempotencyStore,
	identityFunc func(context.Context) string,
	ttl time.Duration,
	logger *slog.Logger,
) connect.UnaryInterceptorFunc {
	if identityFunc == nil {
		identityFunc = defaultIdentity
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}

	interceptor := func(next connect.UnaryFunc) connect.UnaryFunc {
		return connect.UnaryFunc(func(
			ctx context.Context,
			req connect.AnyRequest,
		) (connect.AnyResponse, error) {
			idempotencyKey := req.Header().Get("Idempotency-Key")
			if idempotencyKey == "" {
				return next(ctx, req)
			}

			identity := identityFunc(ctx)
			procedure := req.Spec().Procedure
			scope := fmt.Sprintf("%s:%s", identity, procedure)

			// Compute request hash from the serialized proto payload.
			msg, ok := req.Any().(proto.Message)
			if !ok {
				return nil, connect.NewError(connect.CodeInternal,
					fmt.Errorf("idempotency: request is not a proto.Message"))
			}
			body, err := proto.Marshal(msg)
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal,
					fmt.Errorf("idempotency: marshal request: %w", err))
			}
			hash := sha256Hex(body)

			record := &store.IdempotencyRecord{
				Scope:       scope,
				Key:         sha256Hex([]byte(idempotencyKey)),
				RequestHash: hash,
				ExpiresAt:   time.Now().UTC().Add(ttl),
			}

			_, created, err := idemStore.CreateOrGet(ctx, record)
			if err != nil {
				if errors.Is(err, store.ErrIdempotencyConflict) {
					if logger != nil {
						logger.Warn("idempotency conflict",
							"scope", scope,
							"key", idempotencyKey,
							"request_id", contracts.RequestID(ctx),
						)
					}
					return nil, connect.NewError(connect.CodeAlreadyExists,
						fmt.Errorf("idempotency key conflict: scope=%s key=%s", scope, idempotencyKey))
				}
				return nil, connect.NewError(connect.CodeInternal,
					fmt.Errorf("idempotency check failed: %w", err))
			}

			if !created && logger != nil {
				logger.Info("idempotent replay",
					"scope", scope,
					"key_hash", record.Key,
					"request_id", contracts.RequestID(ctx),
				)
			}

			return next(ctx, req)
		})
	}
	return connect.UnaryInterceptorFunc(interceptor)
}

func defaultIdentity(_ context.Context) string {
	return "anonymous"
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
