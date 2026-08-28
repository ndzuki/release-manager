package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	webhookv1 "github.com/ndzuki/release-manager/api/gen/webhook/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/webhook"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordingBundleHandler captures the actor injected by the interceptor chain
// and answers SubmitBundle with a fixed response.
type recordingBundleHandler struct {
	orchestratorv1connect.UnimplementedBundleServiceHandler

	mu    sync.Mutex
	actor authctx.Actor
}

func (h *recordingBundleHandler) SubmitBundle(ctx context.Context, _ *connect.Request[orchestratorv1.SubmitBundleRequest]) (*connect.Response[orchestratorv1.SubmitBundleResponse], error) {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("actor missing"))
	}
	h.mu.Lock()
	h.actor = actor
	h.mu.Unlock()
	return connect.NewResponse(&orchestratorv1.SubmitBundleResponse{
		Bundle:  &orchestratorv1.BundleSummary{Id: "bundle-001", Name: "dev-release-bundle"},
		Created: true,
	}), nil
}

func (h *recordingBundleHandler) actorSnapshot() authctx.Actor {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.actor
}

// rejectJWTLeg stands in for the JWT interceptor in the TryAll chain: a
// service token is not a JWT, so the JWT path rejects it with
// CodeUnauthenticated and TryAll falls through to the service token path —
// the exact ordering the orchestrator wires (D-100 选项 B).
func rejectJWTLeg() connect.UnaryInterceptorFunc {
	return connect.UnaryInterceptorFunc(func(_ connect.UnaryFunc) connect.UnaryFunc {
		return func(_ context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if req.Header().Get("Authorization") == "" {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing authentication credentials"))
			}
			// A bearer that is not a valid JWT is rejected by the JWT leg.
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid token"))
		}
	})
}

func TestServiceTokenInterceptor_ValidTokenInjectsReleaseWebhookActor(t *testing.T) {
	const rawToken = "dev-service-token"
	interceptor := ServiceTokenInterceptor("release-webhook", []string{SHA256Hash([]byte(rawToken))}, discardLogger())

	var gotActor authctx.Actor
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		actor, ok := authctx.ActorFromContext(ctx)
		if !ok {
			t.Fatal("actor not injected")
		}
		gotActor = actor
		return connect.NewResponse(&orchestratorv1.SubmitBundleResponse{}), nil
	}
	req := connect.NewRequest(&orchestratorv1.SubmitBundleRequest{Name: "bundle"})
	req.Header().Set("Authorization", "Bearer "+rawToken)

	_, err := interceptor(next)(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "release-webhook", gotActor.Service)
	assert.Equal(t, "system", gotActor.UserID)
}

func TestServiceTokenInterceptor_RejectsInvalidAndMissingTokens(t *testing.T) {
	interceptor := ServiceTokenInterceptor("release-webhook", []string{SHA256Hash([]byte("dev-service-token"))}, discardLogger())
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		t.Fatal("next must not be called")
		return nil, nil
	}

	t.Run("wrong token", func(t *testing.T) {
		req := connect.NewRequest(&orchestratorv1.SubmitBundleRequest{Name: "bundle"})
		req.Header().Set("Authorization", "Bearer wrong-token")
		_, err := interceptor(next)(context.Background(), req)
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})

	t.Run("missing token", func(t *testing.T) {
		req := connect.NewRequest(&orchestratorv1.SubmitBundleRequest{Name: "bundle"})
		_, err := interceptor(next)(context.Background(), req)
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("empty allowed set", func(t *testing.T) {
		empty := ServiceTokenInterceptor("release-webhook", nil, discardLogger())
		req := connect.NewRequest(&orchestratorv1.SubmitBundleRequest{Name: "bundle"})
		req.Header().Set("Authorization", "Bearer dev-service-token")
		_, err := empty(next)(context.Background(), req)
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})
}

// TestServiceTokenBundleIngressIntegration covers AC-065-33 end to end at the
// Connect layer: webhook.Service forwards `Authorization: Bearer <token>`
// over real Connect HTTP; the orchestrator-side chain
// TryAllInterceptor(JWT-reject, ServiceTokenInterceptor(serviceTokens()))
// authenticates it and the BundleService handler observes the actor
// service:release-webhook. The negative leg proves the previous real-smoke
// failure mode (no Authorization → unauthenticated) is what a missing
// service token still produces.
func TestServiceTokenBundleIngressIntegration(t *testing.T) {
	const rawToken = "dev-service-token"
	handler := &recordingBundleHandler{}

	mux := http.NewServeMux()
	path, bundleHandler := orchestratorv1connect.NewBundleServiceHandler(
		handler,
		connect.WithInterceptors(
			TryAllInterceptor(discardLogger(),
				rejectJWTLeg(),
				// Mirrors the production mount: the service token is scoped
				// to SubmitReleaseBundle only (REQ-011 §561, v21 Step 6 风险行).
				ServiceTokenInterceptor("release-webhook", []string{SHA256Hash([]byte(rawToken))}, discardLogger(),
					orchestratorv1connect.BundleServiceSubmitBundleProcedure),
			),
		),
	)
	mux.Handle(path, bundleHandler)
	server := httptest.NewServer(mux)
	defer server.Close()

	bundleClient := orchestratorv1connect.NewBundleServiceClient(server.Client(), server.URL)

	t.Run("service token authenticates as service:release-webhook", func(t *testing.T) {
		svc := webhook.NewService(discardLogger(), bundleClient, rawToken)
		req := connect.NewRequest(&webhookv1.SubmitReleaseBundleRequest{
			Name: "dev-release-bundle", ChartDigest: "sha256:abc123",
		})
		req.Header().Set("Idempotency-Key", "bundle-seed-1")
		resp, err := svc.SubmitReleaseBundle(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, resp.Msg.GetBundle())
		assert.True(t, resp.Msg.GetCreated())

		actor := handler.actorSnapshot()
		assert.Equal(t, "release-webhook", actor.Service)
		assert.Equal(t, "system", actor.UserID)
	})

	t.Run("no token configured fails unauthenticated", func(t *testing.T) {
		svc := webhook.NewService(discardLogger(), bundleClient, "")
		req := connect.NewRequest(&webhookv1.SubmitReleaseBundleRequest{
			Name: "dev-release-bundle", ChartDigest: "sha256:abc123",
		})
		_, err := svc.SubmitReleaseBundle(context.Background(), req)
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("valid token outside submit bundle is rejected", func(t *testing.T) {
		// REQ-011 §561: the CI key is scoped to SubmitReleaseBundle — the
		// same valid token on GetBundle must not widen the authorization
		// surface (v21 Step 6 风险行).
		req := connect.NewRequest(&orchestratorv1.GetBundleRequest{BundleId: "bundle-001"})
		req.Header().Set("Authorization", "Bearer "+rawToken)
		_, err := bundleClient.GetBundle(context.Background(), req)
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})
}

// TestServiceTokenPreviousHashAllowsRotation covers REQ-011 §562's
// current+previous dual-hash contract: a token present only in the previous
// slot still authenticates (zero-downtime rotation window).
func TestServiceTokenPreviousHashAllowsRotation(t *testing.T) {
	const previousToken = "previous-dev-service-token"
	interceptor := ServiceTokenInterceptor("release-webhook",
		[]string{SHA256Hash([]byte("current-token")), SHA256Hash([]byte(previousToken))},
		discardLogger())

	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		actor, ok := authctx.ActorFromContext(ctx)
		if !ok {
			t.Fatal("actor not injected")
		}
		assert.Equal(t, "release-webhook", actor.Service)
		return connect.NewResponse(&orchestratorv1.SubmitBundleResponse{}), nil
	}
	req := connect.NewRequest(&orchestratorv1.SubmitBundleRequest{Name: "bundle"})
	req.Header().Set("Authorization", "Bearer "+previousToken)

	_, err := interceptor(next)(context.Background(), req)
	require.NoError(t, err)
}

func TestMapProcedure_BundleService(t *testing.T) {
	if object, action := mapProcedure("/orchestrator.v1.BundleService/SubmitBundle"); object != "bundle" || action != "write" {
		t.Fatalf("mapProcedure(BundleService/SubmitBundle) = (%q, %q), want (\"bundle\", \"write\")", object, action)
	}
	if object, action := mapProcedure("/orchestrator.v1.BundleService/GetBundle"); object != "bundle" || action != "read" {
		t.Fatalf("mapProcedure(BundleService/GetBundle) = (%q, %q), want (\"bundle\", \"read\")", object, action)
	}
}
