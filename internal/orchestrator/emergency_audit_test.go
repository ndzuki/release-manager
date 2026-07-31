
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
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/trust"
)

func TestEmergencyChange_AuditsSuccessAndFailure(t *testing.T) {
	_, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedEmergencyAuditIdentity(t, st)

	logger := slog.New(slog.DiscardHandler)
	emitter := audit.NewEmitter(st.AuditEvents(), logger, audit.EmitterConfig{
		BufferSize: 16, FlushInterval: time.Hour, BatchSize: 16, SpoolPath: t.TempDir() + "/audit.jsonl",
	})
	dispatcher := &recordingEmergencyDispatcher{}
	svc := NewService(st, trust.NewStubVerifier(st.Verifications(), nil, logger), "staging", emitter, dispatcher, logger)
	ctx := emergencyAuditContext()

	successResp, err := svc.EmergencyChange(ctx, emergencyReplicasRequest("audit-success", 3))
	require.NoError(t, err)
	invalid := emergencyReplicasRequest("audit-failure", 3)
	invalid.Msg.Reason = ""
	_, err = svc.EmergencyChange(ctx, invalid)
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

func TestEmergencyChange_AuditDoesNotPersistRawPayload(t *testing.T) {
	_, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedEmergencyAuditIdentity(t, st)

	logger := slog.New(slog.DiscardHandler)
	emitter := audit.NewEmitter(st.AuditEvents(), logger, audit.EmitterConfig{
		BufferSize: 4, FlushInterval: time.Hour, BatchSize: 4, SpoolPath: t.TempDir() + "/audit.jsonl",
	})
	svc := NewService(st, trust.NewStubVerifier(st.Verifications(), nil, logger), "staging", emitter, &recordingEmergencyDispatcher{}, logger)
	req := emergencyReplicasRequest("audit-redaction", 3)
	req.Msg.Reason = "token=secret-value"
	resp, err := svc.EmergencyChange(emergencyAuditContext(), req)
	require.NoError(t, err)
	require.NoError(t, emitter.Shutdown(context.Background()))

	events, err := st.AuditEvents().ListByResource(context.Background(), "operation", resp.Msg.OperationId)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.NotContains(t, events[0].Metadata["reason"], "secret-value")
	assert.Equal(t, store.AuditActorUser, events[0].ActorKind)
}
