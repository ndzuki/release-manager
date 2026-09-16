package auth

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/authctx"
)

// TestServiceTokenScopesCannotBeSubstituted covers REQ-011 §562/AC-011-04/16/17
// after the Harbor key was added: the CI key only opens SubmitBundle, the Harbor
// key only opens RecordArtifactEvent, and an unrecognized token falls through to
// the next credential leg instead of hard-failing it.
func TestServiceTokenScopesCannotBeSubstituted(t *testing.T) {
	const (
		ciToken     = "ci-api-key-value"
		harborToken = "harbor-api-key-value"
	)
	chain := TryAllInterceptor(discardLogger(),
		ServiceTokenInterceptor("release-webhook", TokenHashes(ciToken), discardLogger(),
			orchestratorv1connect.BundleServiceSubmitBundleProcedure),
		ServiceTokenInterceptor("release-harbor", TokenHashes(harborToken), discardLogger(),
			orchestratorv1connect.BundleServiceRecordArtifactEventProcedure),
	)
	//nolint:unparam // the interceptor contract fixes the error result; these cases only assert the decision.
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		actor, ok := authctx.ActorFromContext(ctx)
		require.True(t, ok)
		assert.NotEmpty(t, actor.Service)
		return connect.NewResponse(&orchestratorv1.SubmitBundleResponse{}), nil
	}
	submit := func(token string) connect.AnyRequest {
		req := &submitBundleRequest{Request: connect.NewRequest(&orchestratorv1.SubmitBundleRequest{Name: "bundle"})}
		if token != "" {
			req.Header().Set("Authorization", "Bearer "+token)
		}
		return req
	}
	record := func(token string) connect.AnyRequest {
		req := &recordArtifactRequest{Request: connect.NewRequest(
			&orchestratorv1.RecordArtifactEventRequest{SourceId: "harbor", EventId: "event-1"})}
		if token != "" {
			req.Header().Set("Authorization", "Bearer "+token)
		}
		return req
	}

	tests := []struct {
		name    string
		request connect.AnyRequest
		want    connect.Code
	}{
		{name: "ci key opens SubmitBundle", request: submit(ciToken)},
		{name: "harbor key is refused for SubmitBundle", request: submit(harborToken), want: connect.CodePermissionDenied},
		{name: "harbor key opens RecordArtifactEvent", request: record(harborToken)},
		{name: "ci key is refused for RecordArtifactEvent", request: record(ciToken), want: connect.CodePermissionDenied},
		{name: "unknown key matches no leg", request: submit("unknown-key"), want: connect.CodeUnauthenticated},
		{name: "missing key matches no leg", request: submit(""), want: connect.CodeUnauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := chain(next)(context.Background(), tt.request)
			if tt.want == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.want, connect.CodeOf(err))
		})
	}
}

type submitBundleRequest struct {
	*connect.Request[orchestratorv1.SubmitBundleRequest]
}

func (r *submitBundleRequest) Spec() connect.Spec {
	return connect.Spec{
		Procedure:  orchestratorv1connect.BundleServiceSubmitBundleProcedure,
		StreamType: connect.StreamTypeUnary,
	}
}

type recordArtifactRequest struct {
	*connect.Request[orchestratorv1.RecordArtifactEventRequest]
}

func (r *recordArtifactRequest) Spec() connect.Spec {
	return connect.Spec{
		Procedure:  orchestratorv1connect.BundleServiceRecordArtifactEventProcedure,
		StreamType: connect.StreamTypeUnary,
	}
}
