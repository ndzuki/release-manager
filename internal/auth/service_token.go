// Package auth provides service-to-service authentication.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"hash"
	"log/slog"
	"sync"

	"connectrpc.com/connect"

	"github.com/ndzuki/release-manager/internal/authctx"
)

// ServiceTokenInterceptor validates a Bearer token for backend service-to-service
// communication. The token is compared against a set of allowed hashes using
// constant-time comparison to prevent timing attacks.
func ServiceTokenInterceptor(allowedTokens []string, logger *slog.Logger) connect.UnaryInterceptorFunc {
	if logger == nil {
		logger = slog.Default()
	}
	hashes := make([][]byte, len(allowedTokens))
	for index, token := range allowedTokens {
		bytes, err := hex.DecodeString(token)
		if err != nil {
			logger.Error("invalid service token hex", "error", err)
			continue
		}
		hashes[index] = bytes
	}
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if req.Spec().IsClient {
				return next(ctx, req)
			}
			token := extractToken(req.Header().Get("Authorization"))
			if token == "" {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing service token"))
			}
			if !constantTimeMatch(token, hashes) {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("invalid service token"))
			}
			ctx = authctx.WithActor(ctx, authctx.Actor{
				UserID:  "system",
				Service: "backend",
			})
			return next(ctx, req)
		}
	})
}

// APIKeyInterceptor validates an API key against a set of SHA-256 hashes
// using constant-time comparison. The actor is injected as service:<name>.
func APIKeyInterceptor(allowedKeys []string, serviceName string, logger *slog.Logger) connect.UnaryInterceptorFunc {
	if logger == nil {
		logger = slog.Default()
	}
	hashes := make([][]byte, len(allowedKeys))
	for index, key := range allowedKeys {
		bytes, err := hex.DecodeString(key)
		if err != nil {
			logger.Error("invalid api key hex", "error", err)
			continue
		}
		hashes[index] = bytes
	}
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if req.Spec().IsClient {
				return next(ctx, req)
			}
			token := extractToken(req.Header().Get("Authorization"))
			if token == "" {
				token = extractToken(req.Header().Get("X-Api-Key"))
			}
			if token == "" {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing api key"))
			}
			if !constantTimeMatch(token, hashes) {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("invalid api key"))
			}
			ctx = authctx.WithActor(ctx, authctx.Actor{
				UserID:  serviceName,
				Service: serviceName,
			})
			return next(ctx, req)
		}
	})
}

func constantTimeMatch(candidate string, hashes [][]byte) bool {
	if len(hashes) == 0 {
		return false
	}
	mac := hmac.New(sha256.New, nil)
	mac.Write([]byte(candidate))
	candidateHash := mac.Sum(nil)
	for _, hash := range hashes {
		if hash == nil {
			continue
		}
		if subtle.ConstantTimeCompare(hash, candidateHash) == 1 {
			return true
		}
	}
	return false
}

// SHA256Hash hex-encodes the SHA-256 digest of data.
func SHA256Hash(data []byte) string {
	return hex.EncodeToString(hashBytes(data))
}

func hashBytes(data []byte) []byte {
	h := sha256Pool.Get().(*sha256Hasher)
	defer sha256Pool.Put(h)
	h.sum.Reset()
	h.sum.Write(data)
	return h.sum.Sum(nil)
}

type sha256Hasher struct {
	sum hash.Hash
}

var sha256Pool = &sync.Pool{
	New: func() any {
		return &sha256Hasher{sum: sha256.New()}
	},
}
