package interceptor

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/contracts"
	"github.com/ndzuki/release-manager/internal/store"
)

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

func TestErrorSanitizeInterceptor_DowngradesInternalWrap(t *testing.T) {
	// AC-010-04: a CodeUnavailable error whose %w-wrapped cause does not
	// resolve to a known stable business error carries internal detail and
	// must be downgraded to CodeInternal with the generic message; the detail
	// stays server-side and request_id rides along as error metadata.
	t.Run("wrapped internal cause", func(t *testing.T) {
		internal := fmt.Errorf("resolve customer: %w", errors.New("dial tcp: connection refused"))
		sanitize := NewErrorSanitizeInterceptor(nil)
		handler := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, connect.NewError(connect.CodeUnavailable, internal)
		}

		ctx := contracts.WithRequestID(context.Background(), "req-downgrade")
		_, err := sanitize.WrapUnary(connect.UnaryFunc(handler))(ctx, connect.NewRequest(&commonv1.Pagination{}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))

		var connectErr *connect.Error
		require.True(t, errors.As(err, &connectErr))
		assert.Equal(t, "internal error", connectErr.Message())
		assert.NotContains(t, connectErr.Message(), "resolve customer")
		assert.NotContains(t, connectErr.Message(), "connection refused")
		assert.Equal(t, "req-downgrade", connectErr.Meta().Get(contracts.RequestIDHeader))
	})

	t.Run("wrapped remote connect error", func(t *testing.T) {
		// Mirrors auth/binding_service.go: resolve customer -> get customer ->
		// upstream connect error (nested *connect.Error in the chain).
		upstream := connect.NewError(connect.CodeInternal, errors.New("SELECT * FROM credentials"))
		internal := fmt.Errorf("resolve customer: %w", fmt.Errorf("get customer: %w", upstream))
		sanitize := NewErrorSanitizeInterceptor(nil)
		handler := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, connect.NewError(connect.CodeUnavailable, internal)
		}

		ctx := contracts.WithRequestID(context.Background(), "req-nested")
		_, err := sanitize.WrapUnary(connect.UnaryFunc(handler))(ctx, connect.NewRequest(&commonv1.Pagination{}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
		assert.NotContains(t, err.Error(), "SELECT")
		assert.NotContains(t, err.Error(), "credentials")
	})
}

func TestErrorSanitizeInterceptor_KeepsStableBusinessErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code connect.Code
	}{
		{
			name: "plain cause keeps business code",
			err:  connect.NewError(connect.CodeUnavailable, errors.New("authorization snapshot stale")),
			code: connect.CodeUnavailable,
		},
		{
			name: "wrapped store sentinel keeps business code",
			err:  connect.NewError(connect.CodeNotFound, fmt.Errorf("get binding: %w", store.ErrNotFound)),
			code: connect.CodeNotFound,
		},
		{
			name: "wrapped store structured conflict keeps business code",
			err:  connect.NewError(connect.CodeAborted, fmt.Errorf("update revision: %w", &store.StateVersionConflictError{Expected: 1, Current: 2})),
			code: connect.CodeAborted,
		},
		{
			name: "authorization error with nil cause keeps business code",
			err: connect.NewError(connect.CodePermissionDenied, &auth.PermissionDeniedError{
				AuthorizationError: auth.AuthorizationError{ReasonCode: "permission_denied"},
			}),
			code: connect.CodePermissionDenied,
		},
		{
			name: "authorization error with wrapped cause keeps business code",
			err: connect.NewError(connect.CodeUnavailable, &auth.PolicyUnavailableError{
				AuthorizationError: auth.AuthorizationError{
					ReasonCode: "policy_unavailable",
					Cause:      errors.New("casbin adapter failure"),
				},
			}),
			code: connect.CodeUnavailable,
		},
		{
			name: "client validation wrap under non-unavailable code keeps business code",
			err:  connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid token: %w", errors.New("token is expired"))),
			code: connect.CodeUnauthenticated,
		},
		{
			name: "invalid argument wrap under non-unavailable code keeps business code",
			err:  connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid CSR: %w", errors.New("x509: malformed certificate"))),
			code: connect.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sanitize := NewErrorSanitizeInterceptor(nil)
			handler := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
				return nil, tt.err
			}

			ctx := contracts.WithRequestID(context.Background(), "req-business")
			_, err := sanitize.WrapUnary(connect.UnaryFunc(handler))(ctx, connect.NewRequest(&commonv1.Pagination{}))
			require.Error(t, err)
			assert.Equal(t, tt.code, connect.CodeOf(err))

			var connectErr *connect.Error
			require.True(t, errors.As(err, &connectErr))
			assert.NotEqual(t, "internal error", connectErr.Message())
			assert.Equal(t, "req-business", connectErr.Meta().Get(contracts.RequestIDHeader))
		})
	}
}
