package authorization

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type snapshotHandler struct {
	response *authv1.GetAuthorizationSnapshotResponse
	err      error
	delay    time.Duration
}

func (h *snapshotHandler) GetAuthorizationSnapshot(context.Context, *connect.Request[authv1.GetAuthorizationSnapshotRequest]) (*connect.Response[authv1.GetAuthorizationSnapshotResponse], error) {
	if h.delay > 0 {
		time.Sleep(h.delay)
	}
	if h.err != nil {
		return nil, h.err
	}
	cloned := proto.Clone(h.response)
	response, ok := cloned.(*authv1.GetAuthorizationSnapshotResponse)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, errors.New("clone authorization snapshot response"))
	}
	return connect.NewResponse(response), nil
}

func (h *snapshotHandler) SetCapabilityGrant(context.Context, *connect.Request[authv1.SetCapabilityGrantRequest]) (*connect.Response[authv1.SetCapabilityGrantResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("set capability grant is not used by consumer tests"))
}

func newModuleFixture(t *testing.T, handler *snapshotHandler) (*Module, *sqlitestore.Store, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	path, rpcHandler := authv1connect.NewAuthorizationServiceHandler(handler)
	mux.Handle(path, rpcHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	st := sqlitestore.OpenTest(t)
	client := authv1connect.NewAuthorizationServiceClient(server.Client(), server.URL)
	metrics := NewMetrics(prometheus.NewRegistry())
	module := NewModule(client, st.Authorization(), metrics, slog.New(slog.DiscardHandler), time.Second, 30*time.Second)
	return module, st, server
}

func TestModuleFailsClosedOnInitialCatchupThenAllows(t *testing.T) {
	orgID := "8d7560b5-3f7f-4cbe-9924-1e88656dd0f7"
	customerID := "f4652165-4726-42be-9bc6-fb046cf91a54"
	handler := &snapshotHandler{response: &authv1.GetAuthorizationSnapshotResponse{
		OrganizationId: orgID, CustomerId: customerID, ActorId: "user-1", CanExecuteEmergency: true,
		SourceVersion: 1, PolicyVersion: 1, Checkpoint: 1, Fresh: true,
	}}
	module, _, _ := newModuleFixture(t, handler)
	actor := authctx.Actor{UserID: "user-1", OrganizationID: orgID}

	err := module.AuthorizeWrite(context.Background(), actor, customerID, store.AuthorizationExecuteEmergency)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.Equal(t, "AUTHORIZATION_SNAPSHOT_STALE", reasonCode(err))

	require.NoError(t, module.AuthorizeWrite(context.Background(), actor, customerID, store.AuthorizationExecuteEmergency))
}

func TestModuleRejectsGapRegressionAndTimeout(t *testing.T) {
	orgID := "81a077cb-401f-44e2-af8c-41dd3249b22c"
	customerID := "ba52df7e-4ae6-436a-aeb6-d82fa9123652"
	handler := &snapshotHandler{response: &authv1.GetAuthorizationSnapshotResponse{
		OrganizationId: orgID, CustomerId: customerID, ActorId: "user-1", CanCreateValuesRevision: true,
		SourceVersion: 1, PolicyVersion: 1, Checkpoint: 1, Fresh: true,
	}}
	module, _, _ := newModuleFixture(t, handler)
	actor := authctx.Actor{UserID: "user-1", OrganizationID: orgID}
	require.Error(t, module.AuthorizeWrite(context.Background(), actor, customerID, store.AuthorizationCreateValues))
	require.NoError(t, module.AuthorizeWrite(context.Background(), actor, customerID, store.AuthorizationCreateValues))

	handler.response.SourceVersion = 3
	err := module.AuthorizeWrite(context.Background(), actor, customerID, store.AuthorizationCreateValues)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

	handler.response.SourceVersion = 0
	err = module.AuthorizeWrite(context.Background(), actor, customerID, store.AuthorizationCreateValues)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

	handler.delay = snapshotDeadline + 50*time.Millisecond
	err = module.AuthorizeWrite(context.Background(), actor, customerID, store.AuthorizationCreateValues)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

func TestModuleRejectsServiceActorWithoutScope(t *testing.T) {
	module := NewModule(nil, nil, nil, slog.New(slog.DiscardHandler), time.Second, time.Second)
	err := module.AuthorizeWrite(context.Background(), authctx.Actor{Service: "notifier"}, "", store.AuthorizationExecuteEmergency)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Equal(t, "INVALID_ACTOR_CONTEXT", reasonCode(err))
}

func TestModuleRestartRestoresCheckpointAndFailsClosedUntilCatchup(t *testing.T) {
	orgID := "b43b657d-c218-4df4-8ef0-245994db9d2b"
	customerID := "9c600c4a-871e-41d0-90c2-d1c8f4df4f5c"
	handler := &snapshotHandler{response: &authv1.GetAuthorizationSnapshotResponse{
		OrganizationId: orgID, CustomerId: customerID, ActorId: "user-1", CanExecuteEmergency: true,
		SourceVersion: 1, PolicyVersion: 1, Checkpoint: 1, Fresh: true,
	}}
	first, st, server := newModuleFixture(t, handler)
	actor := authctx.Actor{UserID: "user-1", OrganizationID: orgID}
	require.Error(t, first.AuthorizeWrite(context.Background(), actor, customerID, store.AuthorizationExecuteEmergency))
	require.NoError(t, first.AuthorizeWrite(context.Background(), actor, customerID, store.AuthorizationExecuteEmergency))

	handler.response.SourceVersion = 2
	handler.response.Checkpoint = 2
	client := authv1connect.NewAuthorizationServiceClient(server.Client(), server.URL)
	restarted := NewModule(client, st.Authorization(), NewMetrics(prometheus.NewRegistry()), slog.New(slog.DiscardHandler), time.Second, 30*time.Second)
	err := restarted.AuthorizeWrite(context.Background(), actor, customerID, store.AuthorizationExecuteEmergency)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.NoError(t, restarted.AuthorizeWrite(context.Background(), actor, customerID, store.AuthorizationExecuteEmergency))
}

func TestMetricsHandlerExposesAuthorizationNames(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.Decisions.WithLabelValues("deny", "human").Inc()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", http.NoBody)
	metrics.Handler().ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	for _, name := range []string{
		"auth_decisions_total", "auth_snapshot_stale_total", "auth_source_version",
		"auth_checkpoint_version", "auth_policy_health", "auth_enforce_duration_seconds",
		"auth_snapshot_rpc_duration_seconds",
	} {
		assert.Contains(t, recorder.Body.String(), name)
	}
}

func TestTranslateSnapshotErrorPreservesPermissionDenied(t *testing.T) {
	denied := connect.NewError(connect.CodePermissionDenied, errors.New("denied"))
	assert.Same(t, denied, translateSnapshotError(denied))
}

var _ authv1connect.AuthorizationServiceHandler = (*snapshotHandler)(nil)
