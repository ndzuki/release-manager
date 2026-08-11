package contracts

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestID(t *testing.T) {
	ctx := context.Background()
	assert.Empty(t, RequestID(ctx))

	ctx = WithRequestID(ctx, "req-123")
	assert.Equal(t, "req-123", RequestID(ctx))
}

func TestOperationID(t *testing.T) {
	ctx := context.Background()
	assert.Empty(t, OperationID(ctx))

	ctx = WithOperationID(ctx, "op-456")
	assert.Equal(t, "op-456", OperationID(ctx))
}

func TestNewAppError(t *testing.T) {
	t.Run("injects request_id as metadata", func(t *testing.T) {
		ctx := WithRequestID(context.Background(), "req-abc")
		err := NewAppError(ctx, connect.CodeNotFound, "customer not found")

		require.NotNil(t, err)
		assert.Equal(t, connect.CodeNotFound, err.Code())
		assert.Equal(t, "customer not found", err.Message())
		assert.Equal(t, "req-abc", err.Meta().Get(RequestIDHeader))
	})

	t.Run("without request_id leaves metadata empty", func(t *testing.T) {
		err := NewAppError(context.Background(), connect.CodeInternal, "oops")
		assert.Equal(t, connect.CodeInternal, err.Code())
		assert.Equal(t, "oops", err.Message())
		assert.Empty(t, err.Meta().Get(RequestIDHeader))
	})
}

func TestNewAppErrorf(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req-001")
	err := NewAppErrorf(ctx, connect.CodeInvalidArgument, "field %s is required", "name")

	assert.Equal(t, connect.CodeInvalidArgument, err.Code())
	assert.Equal(t, "field name is required", err.Message())
	assert.Equal(t, "req-001", err.Meta().Get(RequestIDHeader))
}

func TestToConnectError(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req-xyz")

	t.Run("nil error", func(t *testing.T) {
		assert.Nil(t, ToConnectError(ctx, nil))
	})

	t.Run("connect error keeps code and message", func(t *testing.T) {
		orig := connect.NewError(connect.CodePermissionDenied, errors.New("access denied"))
		sanitized := ToConnectError(ctx, orig)
		assert.Equal(t, connect.CodePermissionDenied, sanitized.Code())
		assert.Equal(t, "access denied", sanitized.Message())
		assert.Equal(t, "req-xyz", sanitized.Meta().Get(RequestIDHeader))
	})

	t.Run("wrapped connect error is unwrapped", func(t *testing.T) {
		orig := connect.NewError(connect.CodeNotFound, errors.New("gone"))
		wrapped := fmt.Errorf("wrap: %w", orig)
		sanitized := ToConnectError(ctx, wrapped)
		assert.Equal(t, connect.CodeNotFound, sanitized.Code())
		assert.Equal(t, "gone", sanitized.Message())
	})

	t.Run("plain error maps to CodeInternal with generic message", func(t *testing.T) {
		sanitized := ToConnectError(ctx, errors.New("something broke: SELECT * FROM secrets"))
		assert.Equal(t, connect.CodeInternal, sanitized.Code())
		assert.Equal(t, "internal error", sanitized.Message())
		assert.Equal(t, "req-xyz", sanitized.Meta().Get(RequestIDHeader))
	})
}
