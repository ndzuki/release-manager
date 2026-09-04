package orchestrator

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/trust"
)

func TestExecuteEmergencyChange_AuditsSuccessAndFailure(t *testing.T) {
	_, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedEmergencyAuditIdentity(t, st)
	seedEmergencyImageIdentity(t, st)
	require.NoError(t, st.EmergencyConfig().SetEmergencyConfig(t.Context(), store.EmergencyConfig{Enabled: true}))
	seedEmergencyTestArtifact(t, st)

	logger := slog.New(slog.DiscardHandler)
	emitter := audit.NewEmitter(st.AuditEvents(), logger, audit.EmitterConfig{
		BufferSize: 16, FlushInterval: time.Hour, BatchSize: 16, SpoolPath: t.TempDir() + "/audit.jsonl",
	})
	dispatcher := &recordingEmergencyDispatcher{}
	uowStore, ok := st.(interface {
		store.Store
		OperationCreationUnitOfWork() store.OperationCreationUnitOfWork
	})
	require.True(t, ok)
	svc := NewService(st, trust.NewStubVerifier(st.Verifications(), nil, logger), "staging", emitter, dispatcher, authorization.NewStoreAuthorizer(st), uowStore.OperationCreationUnitOfWork(), logger)
	ctx := emergencyAuditContext()

	successResp, err := svc.ExecuteEmergencyChange(ctx, emergencyImageRequest("audit-success"))
	require.NoError(t, err)
	invalid := emergencyImageRequest("audit-failure")
	invalid.Msg.ArtifactRef = ""
	_, err = svc.ExecuteEmergencyChange(ctx, invalid)
	require.Error(t, err)

	require.NoError(t, emitter.Shutdown(context.Background()))
	successEvents, err := st.AuditEvents().ListByResource(context.Background(), "operation", successResp.Msg.OperationId)
	require.NoError(t, err)
	require.Len(t, successEvents, 1)
	assert.Equal(t, "succeeded", successEvents[0].Status)
	assert.Equal(t, "emergency_change", successEvents[0].Action)
	assert.Equal(t, "release-admin", successEvents[0].ActorID)

	failureEvents, err := st.AuditEvents().ListByResource(context.Background(), "operation", "def-001")
	require.NoError(t, err)
	require.Len(t, failureEvents, 1)
	assert.Equal(t, "failed", failureEvents[0].Status)
	assert.Equal(t, connect.CodeInvalidArgument.String(), failureEvents[0].Metadata["error_code"])
}

// AC-081-03 (ADR-010): a set_replicas acceptance audit event labels the
// resolved action SET_REPLICAS instead of the previously hard-coded
// SET_CONTAINER_IMAGE.
func TestExecuteEmergencyChangeSetReplicasAuditsResolvedAction(t *testing.T) {
	_, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedEmergencyAuditIdentity(t, st)
	require.NoError(t, st.EmergencyConfig().SetEmergencyConfig(t.Context(), store.EmergencyConfig{Enabled: true}))
	seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)

	logger := slog.New(slog.DiscardHandler)
	emitter := audit.NewEmitter(st.AuditEvents(), logger, audit.EmitterConfig{
		BufferSize: 4, FlushInterval: time.Hour, BatchSize: 4, SpoolPath: t.TempDir() + "/audit.jsonl",
	})
	uowStore, ok := st.(interface {
		store.Store
		OperationCreationUnitOfWork() store.OperationCreationUnitOfWork
	})
	require.True(t, ok)
	svc := NewService(st, trust.NewStubVerifier(st.Verifications(), nil, logger), "staging", emitter, &recordingEmergencyDispatcher{}, authorization.NewStoreAuthorizer(st), uowStore.OperationCreationUnitOfWork(), logger)

	resp, err := svc.ExecuteEmergencyChange(emergencyAuditContext(), emergencyReplicasRequest("audit-replicas", 2))
	require.NoError(t, err)
	require.NoError(t, emitter.Shutdown(context.Background()))

	events, err := st.AuditEvents().ListByResource(context.Background(), "operation", resp.Msg.OperationId)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "succeeded", events[0].Status)
	assert.Contains(t, events[0].ChangeSummary, "action=set_replicas")
	assert.NotContains(t, events[0].ChangeSummary, "set_container_image")
}

func seedEmergencyAuditIdentity(t *testing.T, st store.Store) {
	t.Helper()
	require.NoError(t, st.Users().Create(t.Context(), &store.User{ID: "release-admin", Username: "release-admin", Status: store.UserActive}))
	require.NoError(t, st.OrgMembers().Create(t.Context(), &store.OrganizationMember{OrgID: "org-001", UserID: "release-admin", Role: store.RoleReleaseAdmin}))
	require.NoError(t, st.Clusters().Create(t.Context(), &store.Cluster{ID: "cls-audit", Name: "cls-audit", CustomerID: "cust-001"}))
	definition, err := st.Definitions().Get(t.Context(), "def-001")
	require.NoError(t, err)
	definition.ClusterID = "cls-audit"
	_, err = st.Definitions().Update(t.Context(), definition, nil)
	require.NoError(t, err)
	require.NoError(t, st.Operators().Create(t.Context(), &store.Operator{ID: "op-audit", Name: "op-audit", CustomerID: "cust-001", ClusterID: "cls-audit"}))
	require.NoError(t, st.Sessions().Create(t.Context(), &store.Session{
		ID: uuid.NewString(), OperatorID: "op-audit", Status: store.SessionOnline,
		StartedAt: time.Now().UTC(), LastHeartbeat: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour),
	}))
}

func emergencyAuditContext() context.Context {
	return authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "release-admin", OrganizationID: "org-001", Roles: []string{string(store.RoleReleaseAdmin)},
	})
}

func TestExecuteEmergencyChange_AuditDoesNotPersistRawPayload(t *testing.T) {
	_, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedEmergencyAuditIdentity(t, st)
	seedEmergencyImageIdentity(t, st)
	require.NoError(t, st.EmergencyConfig().SetEmergencyConfig(t.Context(), store.EmergencyConfig{Enabled: true}))
	seedEmergencyTestArtifact(t, st)

	logger := slog.New(slog.DiscardHandler)
	emitter := audit.NewEmitter(st.AuditEvents(), logger, audit.EmitterConfig{
		BufferSize: 4, FlushInterval: time.Hour, BatchSize: 4, SpoolPath: t.TempDir() + "/audit.jsonl",
	})
	uowStore, ok := st.(interface {
		store.Store
		OperationCreationUnitOfWork() store.OperationCreationUnitOfWork
	})
	require.True(t, ok)
	svc := NewService(st, trust.NewStubVerifier(st.Verifications(), nil, logger), "staging", emitter, &recordingEmergencyDispatcher{}, authorization.NewStoreAuthorizer(st), uowStore.OperationCreationUnitOfWork(), logger)
	resp, err := svc.ExecuteEmergencyChange(emergencyAuditContext(), emergencyImageRequest("audit-redaction"))
	require.NoError(t, err)
	require.NoError(t, emitter.Shutdown(context.Background()))

	events, err := st.AuditEvents().ListByResource(context.Background(), "operation", resp.Msg.OperationId)
	require.NoError(t, err)
	require.Len(t, events, 1)
	// The audit event carries identifiers only; no request payload fields
	// (artifact_ref / workload_ref / container values) leak into metadata.
	for _, value := range events[0].Metadata {
		assert.NotContains(t, value, "artifact-img")
		assert.NotContains(t, value, "deployments/default/api")
	}
	assert.Equal(t, store.AuditActorUser, events[0].ActorKind)
}
