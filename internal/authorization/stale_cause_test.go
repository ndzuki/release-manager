package authorization

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

const (
	staleOrgID      = "1a2b3c4d-0000-4000-8000-0000000000aa"
	staleCustomerID = "1a2b3c4d-0000-4000-8000-0000000000bb"
)

// failingCheckpointStore injects checkpoint read/write failures while delegating every
// other method to the real store.
type failingCheckpointStore struct {
	store.AuthorizationStore
	readErr  error
	writeErr error
}

func (s *failingCheckpointStore) GetCheckpoint(ctx context.Context, organizationID, customerID string) (*store.AuthorizationCheckpoint, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	return s.AuthorizationStore.GetCheckpoint(ctx, organizationID, customerID)
}

func (s *failingCheckpointStore) SaveCheckpoint(ctx context.Context, checkpoint store.AuthorizationCheckpoint) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	return s.AuthorizationStore.SaveCheckpoint(ctx, checkpoint)
}

type staleCauseCase struct {
	name string
	// want is the cause the failed AuthorizeWrite must report.
	want StaleCause
	// handler configures the snapshot RPC stub.
	handler func(*snapshotHandler)
	// checkpointErr injects a checkpoint store failure.
	readErr, writeErr error
	// seed writes a pre-existing checkpoint (regression/gap fixtures).
	seed func(*testing.T, *sqlitestore.Store)
	// expectSnapshotCalls guards against fixtures that never reach the pull.
	expectSnapshotCalls int
}

func staleCauseResponse(actorID string) *authv1.GetAuthorizationSnapshotResponse {
	return &authv1.GetAuthorizationSnapshotResponse{
		OrganizationId: staleOrgID,
		CustomerId:     staleCustomerID,
		ActorId:        actorID,
		SourceVersion:  3,
		PolicyVersion:  3,
		Checkpoint:     3,
		Fresh:          true,
	}
}

// TestStaleCauseIsReportedPerFailurePoint is AC-239-01: every pull failure point maps to
// its own cause, and the error keeps the fail-closed contract (AC-239-03).
func TestStaleCauseIsReportedPerFailurePoint(t *testing.T) {
	cases := []staleCauseCase{
		{
			name: "snapshot-rpc-error",
			want: StaleCauseSnapshotRPCError,
			handler: func(h *snapshotHandler) {
				h.err = connect.NewError(connect.CodeUnavailable, errors.New("auth load shed"))
			},
			expectSnapshotCalls: 1,
		},
		{
			name: "snapshot-rpc-timeout",
			want: StaleCauseSnapshotRPCTimeout,
			handler: func(h *snapshotHandler) {
				h.response = staleCauseResponse("user-stale-cause")
				h.delay = snapshotDeadline + 150*time.Millisecond
			},
			// The handler is still in flight when the client gives up at snapshotDeadline,
			// so its call counter is deliberately not read here (that would race).
		},
		{
			name: "snapshot-scope-mismatch",
			want: StaleCauseScopeMismatch,
			handler: func(h *snapshotHandler) {
				response := staleCauseResponse("user-stale-cause")
				response.CustomerId = "1a2b3c4d-0000-4000-8000-0000000000cc"
				h.response = response
			},
			expectSnapshotCalls: 1,
		},
		{
			name:    "checkpoint-read-error",
			want:    StaleCauseCheckpointRead,
			handler: func(h *snapshotHandler) { h.response = staleCauseResponse("user-stale-cause") },
			readErr: errors.New("checkpoint store unavailable"),
		},
		{
			name:     "checkpoint-write-error",
			want:     StaleCauseCheckpointWrite,
			handler:  func(h *snapshotHandler) { h.response = staleCauseResponse("user-stale-cause") },
			writeErr: errors.New("checkpoint store read-only"),
		},
		{
			name:    "warmup",
			want:    StaleCauseWarmup,
			handler: func(h *snapshotHandler) { h.response = staleCauseResponse("user-stale-cause") },
		},
		{
			name: "gap",
			want: StaleCauseGap,
			handler: func(h *snapshotHandler) {
				response := staleCauseResponse("user-stale-cause")
				response.Checkpoint = 5
				response.SourceVersion = 5
				response.PolicyVersion = 5
				h.response = response
			},
			seed: func(t *testing.T, st *sqlitestore.Store) {
				require.NoError(t, st.Authorization().SaveCheckpoint(context.Background(), store.AuthorizationCheckpoint{
					OrganizationID: staleOrgID, CustomerID: staleCustomerID,
					SourceVersion: 1, PolicyVersion: 1, Fresh: true,
				}))
			},
		},
		{
			name:    "regression",
			want:    StaleCauseRegression,
			handler: func(h *snapshotHandler) { h.response = staleCauseResponse("user-stale-cause") },
			seed: func(t *testing.T, st *sqlitestore.Store) {
				require.NoError(t, st.Authorization().SaveCheckpoint(context.Background(), store.AuthorizationCheckpoint{
					OrganizationID: staleOrgID, CustomerID: staleCustomerID,
					SourceVersion: 9, PolicyVersion: 9, Fresh: true,
				}))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := &snapshotHandler{}
			tc.handler(handler)
			module, st, _ := newModuleFixture(t, handler)
			if tc.readErr != nil || tc.writeErr != nil {
				module = newModuleWithCheckpoints(t, handler, &failingCheckpointStore{
					AuthorizationStore: st.Authorization(), readErr: tc.readErr, writeErr: tc.writeErr,
				})
			}
			if tc.seed != nil {
				tc.seed(t, st)
			}

			actor := authctx.Actor{UserID: "user-stale-cause", OrganizationID: staleOrgID}
			err := module.AuthorizeWrite(context.Background(), actor, staleCustomerID, store.AuthorizationExecuteEmergency)
			require.Error(t, err, "the request must still be rejected (fail-closed)")

			// AC-239-03: the wire contract is unchanged.
			assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
			assert.Equal(t, "AUTHORIZATION_SNAPSHOT_STALE", reasonCode(err))
			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			assert.Equal(t, string(tc.want), connectErr.Meta().Get("X-Stale-Cause"))

			// AC-239-03: the ADR-016 fixed counter still increments.
			assert.Equal(t, float64(1), testutil.ToFloat64(module.metrics.SnapshotStale))
			// AC-239-01: the new breakdown carries the cause.
			assert.Equal(t, float64(1), testutil.ToFloat64(module.metrics.SnapshotStaleCause.WithLabelValues(string(tc.want))))
			if tc.expectSnapshotCalls > 0 {
				assert.Equal(t, tc.expectSnapshotCalls, handler.calls)
			}
		})
	}
}

// TestStaleCauseMetricStaysBounded is AC-239-02: the cause label only ever carries an
// enum value — no free text, no empty value — and its cardinality is capped.
func TestStaleCauseMetricStaysBounded(t *testing.T) {
	allowed := map[string]bool{}
	for _, cause := range StaleCauses() {
		allowed[string(cause)] = true
	}

	metrics := NewMetrics(prometheus.NewRegistry())
	for _, cause := range StaleCauses() {
		metrics.SnapshotStaleCause.WithLabelValues(string(cause)).Inc()
	}
	assert.LessOrEqual(t, testutil.CollectAndCount(metrics.SnapshotStaleCause), len(StaleCauses()))

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", http.NoBody))
	require.Equal(t, http.StatusOK, recorder.Code)

	label := regexp.MustCompile(`auth_snapshot_stale_cause_total\{cause="([^"]*)"\}`)
	matches := label.FindAllStringSubmatch(recorder.Body.String(), -1)
	require.NotEmpty(t, matches, "the cause metric must be exported")
	for _, match := range matches {
		assert.NotEmpty(t, match[1], "the cause label must never be empty")
		assert.True(t, allowed[match[1]], "cause %q must be part of the enum", match[1])
	}
}

// TestStaleCauseIsLoggedAtWarn is AC-239-04: a failing authorization path names the
// cause in a WARN record, so CI artifacts (and the runbook's reading recipe) can tell the
// causes apart. A healthy warm-up must stay below Error level.
func TestStaleCauseIsLoggedAtWarn(t *testing.T) {
	handler := &snapshotHandler{err: connect.NewError(connect.CodeUnavailable, errors.New("auth load shed"))}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	module := newModuleWithLogger(t, handler, logger)

	actor := authctx.Actor{UserID: "user-stale-log", OrganizationID: staleOrgID}
	err := module.AuthorizeWrite(context.Background(), actor, staleCustomerID, store.AuthorizationExecuteEmergency)
	require.Error(t, err)

	logged := logs.String()
	assert.Contains(t, logged, `"level":"WARN"`)
	assert.Contains(t, logged, `"stale_cause":"snapshot-rpc-error"`)
	assert.Contains(t, logged, `"reason":"AUTHORIZATION_SNAPSHOT_STALE"`)
	assert.NotContains(t, logged, `"level":"ERROR"`, "a rejection is not an application error")
	// The raw pull error must still not leak through the returned error.
	assert.NotContains(t, err.Error(), "auth load shed")
}

// TestWarmupStaleCauseIsWarnNotError pins the healthy-path level: the first pull of a
// fresh module is expected to be stale, and that must never be logged as an error.
func TestWarmupStaleCauseIsWarnNotError(t *testing.T) {
	handler := &snapshotHandler{response: staleCauseResponse("user-warmup")}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	module := newModuleWithLogger(t, handler, logger)

	actor := authctx.Actor{UserID: "user-warmup", OrganizationID: staleOrgID}
	require.Error(t, module.AuthorizeWrite(context.Background(), actor, staleCustomerID, store.AuthorizationExecuteEmergency))
	assert.Contains(t, logs.String(), `"stale_cause":"warmup"`)
	assert.NotContains(t, logs.String(), `"level":"ERROR"`)
}

func newModuleWithLogger(t *testing.T, handler *snapshotHandler, logger *slog.Logger) *Module {
	t.Helper()
	mux := http.NewServeMux()
	path, rpcHandler := authv1connect.NewAuthorizationServiceHandler(handler)
	mux.Handle(path, rpcHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	st := sqlitestore.OpenTest(t)
	client := authv1connect.NewAuthorizationServiceClient(server.Client(), server.URL)
	return NewModule(client, st.Authorization(), NewMetrics(prometheus.NewRegistry()), logger, time.Second, 30*time.Second)
}

func newModuleWithCheckpoints(t *testing.T, handler *snapshotHandler, checkpoints store.AuthorizationStore) *Module {
	t.Helper()
	mux := http.NewServeMux()
	path, rpcHandler := authv1connect.NewAuthorizationServiceHandler(handler)
	mux.Handle(path, rpcHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := authv1connect.NewAuthorizationServiceClient(server.Client(), server.URL)
	return NewModule(client, checkpoints, NewMetrics(prometheus.NewRegistry()), slog.New(slog.DiscardHandler), time.Second, 30*time.Second)
}
