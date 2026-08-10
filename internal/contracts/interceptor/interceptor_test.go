package interceptor

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/contracts"
	"github.com/ndzuki/release-manager/internal/store"
)

type fakeIdempotencyStore struct {
	records map[string]*store.IdempotencyRecord
}

func newFakeIdempotencyStore() *fakeIdempotencyStore {
	return &fakeIdempotencyStore{records: make(map[string]*store.IdempotencyRecord)}
}

func (s *fakeIdempotencyStore) CreateOrGet(_ context.Context, record *store.IdempotencyRecord) (*store.IdempotencyRecord, bool, error) {
	compositeKey := record.Scope + "|" + record.Key
	if existing, ok := s.records[compositeKey]; ok {
		if existing.RequestHash != record.RequestHash {
			return nil, false, store.ErrIdempotencyConflict
		}
		return existing, false, nil
	}
	s.records[compositeKey] = record
	return record, true, nil
}

func TestNewRequestIDInterceptor(t *testing.T) {
	t.Run("generates UUID when no header", func(t *testing.T) {
		interceptor := NewRequestIDInterceptor(nil)
		handler := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			rid := contracts.RequestID(ctx)
			assert.NotEmpty(t, rid)
			return connect.NewResponse(&commonv1.Pagination{}), nil
		}

		wrapped := interceptor.WrapUnary(connect.UnaryFunc(handler))
		req := connect.NewRequest(&commonv1.Pagination{PageSize: 10})

		resp, err := wrapped(context.Background(), req)
		require.NoError(t, err)
		assert.NotEmpty(t, resp.Header().Get("X-Request-Id"))
	})

	t.Run("echoes existing X-Request-Id", func(t *testing.T) {
		interceptor := NewRequestIDInterceptor(nil)
		handler := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			assert.Equal(t, "my-custom-id", contracts.RequestID(ctx))
			return connect.NewResponse(&commonv1.Pagination{}), nil
		}

		wrapped := interceptor.WrapUnary(connect.UnaryFunc(handler))
		req := connect.NewRequest(&commonv1.Pagination{PageSize: 10})
		req.Header().Set("X-Request-Id", "my-custom-id")

		resp, err := wrapped(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, "my-custom-id", resp.Header().Get("X-Request-Id"))
	})
}

func TestNewIdempotencyInterceptor(t *testing.T) {
	t.Run("passthrough when no idempotency header", func(t *testing.T) {
		idem := newFakeIdempotencyStore()
		interceptor := NewIdempotencyInterceptor(idem, nil, time.Hour, nil)

		handler := func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return connect.NewResponse(&commonv1.Pagination{PageSize: 20}), nil
		}

		wrapped := interceptor(connect.UnaryFunc(handler))
		req := connect.NewRequest(&commonv1.Pagination{PageSize: 10})

		resp, err := wrapped(context.Background(), req)
		require.NoError(t, err)
		assert.NotNil(t, resp)
	})

	t.Run("first call creates record", func(t *testing.T) {
		idem := newFakeIdempotencyStore()
		customIdentity := func(_ context.Context) string { return "user-42" }
		interceptor := NewIdempotencyInterceptor(idem, customIdentity, time.Hour, nil)
		callCount := 0
		handler := func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			callCount++
			return connect.NewResponse(&commonv1.Pagination{}), nil
		}

		wrapped := interceptor(connect.UnaryFunc(handler))
		req := connect.NewRequest(&commonv1.Pagination{PageSize: 10})
		req.Header().Set("Idempotency-Key", "key-1")

		_, err := wrapped(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, 1, callCount)
	})

	t.Run("idempotent replay calls handler again", func(t *testing.T) {
		idem := newFakeIdempotencyStore()
		interceptor := NewIdempotencyInterceptor(idem, nil, time.Hour, nil)

		callCount := 0
		handler := func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			callCount++
			return connect.NewResponse(&commonv1.Pagination{PageSize: 20}), nil
		}
		wrapped := interceptor(connect.UnaryFunc(handler))

		req1 := connect.NewRequest(&commonv1.Pagination{PageSize: 10})
		req1.Header().Set("Idempotency-Key", "replay-key")
		_, err := wrapped(context.Background(), req1)
		require.NoError(t, err)
		assert.Equal(t, 1, callCount)

		req2 := connect.NewRequest(&commonv1.Pagination{PageSize: 10})
		req2.Header().Set("Idempotency-Key", "replay-key")
		_, err = wrapped(context.Background(), req2)
		require.NoError(t, err)
		assert.Equal(t, 2, callCount)
	})
}

func TestNewIdempotencyInterceptor_Conflict(t *testing.T) {
	idem := newFakeIdempotencyStore()
	interceptor := NewIdempotencyInterceptor(idem, nil, time.Hour, nil)

	handler := func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&commonv1.Pagination{}), nil
	}
	wrapped := interceptor(connect.UnaryFunc(handler))

	req1 := connect.NewRequest(&commonv1.Pagination{PageSize: 10})
	req1.Header().Set("Idempotency-Key", "conflict-key")
	_, err := wrapped(context.Background(), req1)
	require.NoError(t, err)

	req2 := connect.NewRequest(&commonv1.Pagination{PageSize: 50})
	req2.Header().Set("Idempotency-Key", "conflict-key")
	_, err = wrapped(context.Background(), req2)
	require.Error(t, err)

	var connectErr *connect.Error
	require.True(t, errors.As(err, &connectErr))
	assert.Equal(t, connect.CodeAlreadyExists, connectErr.Code())
	assert.Contains(t, connectErr.Message(), "idempotency key conflict")
}

func TestDefaultIdentity(t *testing.T) {
	assert.Equal(t, "anonymous", defaultIdentity(context.Background()))
}

func TestSha256Hex(t *testing.T) {
	h := sha256Hex([]byte("hello"))
	assert.Len(t, h, 64)
	assert.Equal(t, h, sha256Hex([]byte("hello")))
	assert.NotEqual(t, h, sha256Hex([]byte("world")))
}
