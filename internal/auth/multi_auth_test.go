package auth

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
)

// recordingLeg tracks whether a candidate interceptor ran and returns a
// canned error/response.
type recordingLeg struct {
	ran bool
	err error
}

func (l *recordingLeg) interceptor() connect.UnaryInterceptorFunc {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			l.ran = true
			if l.err != nil {
				return nil, l.err
			}
			return next(ctx, req)
		}
	})
}

// TestTryAllInterceptor_FallthroughSemantics locks the REQ-011 §562 ordering
// contract: Unauthenticated falls through to the next leg; PermissionDenied
// (authenticated but unauthorized) propagates without consulting later legs;
// all-authentication-failures collapse to unauthenticated.
func TestTryAllInterceptor_FallthroughSemantics(t *testing.T) {
	handler := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&orchestratorv1.SubmitBundleResponse{}), nil
	}
	req := connect.NewRequest(&orchestratorv1.SubmitBundleRequest{Name: "bundle"})

	t.Run("unauthenticated falls through to next leg", func(t *testing.T) {
		first := &recordingLeg{err: connect.NewError(connect.CodeUnauthenticated, errors.New("not a jwt"))}
		second := &recordingLeg{}
		chain := TryAllInterceptor(discardLogger(), first.interceptor(), second.interceptor())
		_, err := chain(handler)(context.Background(), req)
		require.NoError(t, err)
		assert.True(t, first.ran, "first leg must run")
		assert.True(t, second.ran, "second leg must run after an unauthenticated first leg")
	})

	t.Run("permission denied propagates immediately", func(t *testing.T) {
		first := &recordingLeg{err: connect.NewError(connect.CodePermissionDenied, errors.New("casbin denied"))}
		second := &recordingLeg{}
		chain := TryAllInterceptor(discardLogger(), first.interceptor(), second.interceptor())
		_, err := chain(handler)(context.Background(), req)
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		assert.False(t, second.ran, "a later leg must not re-judge an authenticated-but-denied request")
	})

	t.Run("non-auth error propagates immediately", func(t *testing.T) {
		first := &recordingLeg{err: connect.NewError(connect.CodeInternal, errors.New("boom"))}
		second := &recordingLeg{}
		chain := TryAllInterceptor(discardLogger(), first.interceptor(), second.interceptor())
		_, err := chain(handler)(context.Background(), req)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
		assert.False(t, second.ran)
	})

	t.Run("all legs unauthenticated collapses to unauthenticated", func(t *testing.T) {
		first := &recordingLeg{err: connect.NewError(connect.CodeUnauthenticated, errors.New("not a jwt"))}
		second := &recordingLeg{err: connect.NewError(connect.CodeUnauthenticated, errors.New("missing service token"))}
		chain := TryAllInterceptor(discardLogger(), first.interceptor(), second.interceptor())
		_, err := chain(handler)(context.Background(), req)
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
		assert.True(t, first.ran && second.ran)
	})
}
