package contracts

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestID(t *testing.T) {
	ctx := context.Background()
	assert.Empty(t, RequestID(ctx))

	ctx = context.WithValue(ctx, RequestIDKey, "req-123")
	assert.Equal(t, "req-123", RequestID(ctx))
}

func TestOperationID(t *testing.T) {
	ctx := context.Background()
	assert.Empty(t, OperationID(ctx))

	ctx = context.WithValue(ctx, OperationIDKey, "op-456")
	assert.Equal(t, "op-456", OperationID(ctx))
}

func TestNewAppError(t *testing.T) {
	t.Run("with request_id", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), RequestIDKey, "req-abc")
		err := NewAppError(ctx, connect.CodeNotFound, "customer not found")

		require.NotNil(t, err)
		assert.Equal(t, connect.CodeNotFound, err.Code())
		assert.Contains(t, err.Message(), "req-abc")
		assert.Contains(t, err.Message(), "customer not found")
	})

	t.Run("without request_id", func(t *testing.T) {
		err := NewAppError(context.Background(), connect.CodeInternal, "oops")
		assert.Equal(t, connect.CodeInternal, err.Code())
		assert.Contains(t, err.Message(), "oops")
	})
}

func TestNewAppErrorf(t *testing.T) {
	ctx := context.WithValue(context.Background(), RequestIDKey, "req-001")
	err := NewAppErrorf(ctx, connect.CodeInvalidArgument, "field %s is required", "name")

	assert.Equal(t, connect.CodeInvalidArgument, err.Code())
	assert.Contains(t, err.Message(), "req-001")
	assert.Contains(t, err.Message(), "name is required")
}

func TestToConnectError(t *testing.T) {
	ctx := context.WithValue(context.Background(), RequestIDKey, "req-xyz")

	t.Run("nil error", func(t *testing.T) {
		assert.Nil(t, ToConnectError(ctx, nil))
	})

	t.Run("connect error", func(t *testing.T) {
		orig := connect.NewError(connect.CodePermissionDenied, errors.New("access denied"))
		sanitized := ToConnectError(ctx, orig)
		assert.Equal(t, connect.CodePermissionDenied, sanitized.Code())
		assert.Contains(t, sanitized.Message(), "req-xyz")
		assert.Contains(t, sanitized.Message(), "access denied")
	})

	t.Run("wrapped connect error", func(t *testing.T) {
		orig := connect.NewError(connect.CodeNotFound, errors.New("gone"))
		wrapped := errors.New("wrap: " + orig.Error())
		// Note: connect.Error.Error() loses the type, so wrapping with %v doesn't preserve it.
		// For real usage, use %w or errors.Join.
		sanitized := ToConnectError(ctx, wrapped)
		assert.Equal(t, connect.CodeInternal, sanitized.Code())
	})

	t.Run("plain error", func(t *testing.T) {
		sanitized := ToConnectError(ctx, errors.New("something broke"))
		assert.Equal(t, connect.CodeInternal, sanitized.Code())
		assert.Contains(t, sanitized.Message(), "internal error")
	})
}
