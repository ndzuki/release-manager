package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	auditv1 "github.com/ndzuki/release-manager/api/gen/audit/v1"
	auditv1connect "github.com/ndzuki/release-manager/api/gen/audit/v1/auditv1connect"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestAPISvcAuditConnectEndToEnd(t *testing.T) {
	const signingKey = "test-signing-key"
	dbPath := t.TempDir() + "/api.db"
	mux := http.NewServeMux()
	svc := &apiSvc{dbPath: dbPath, signingKey: signingKey}
	require.NoError(t, svc.Register(mux, slog.Default()))
	t.Cleanup(func() { require.NoError(t, svc.Close(context.Background())) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	jwtManager := auth.NewJWTManager([]byte(signingKey), time.Hour, 24*time.Hour)
	token, _, err := jwtManager.GenerateAccessToken(
		"user-001",
		"org-001",
		[]string{string(store.RoleReleaseAdmin)},
	)
	require.NoError(t, err)
	client := auditv1connect.NewAuditServiceClient(http.DefaultClient, server.URL)
	now := time.Now().UTC()

	emitRequest := connect.NewRequest(&auditv1.EmitAuditRequest{Events: []*auditv1.AuditEvent{{
		Id: "event-001",
		Actor: &auditv1.AuditActor{
			Kind:           auditv1.ActorKind_ACTOR_KIND_USER,
			Id:             "user-001",
			OrganizationId: "org-001",
			Role:           string(store.RoleReleaseAdmin),
		},
		ResourceType: "release_operation",
		ResourceId:   "operation-001",
		Action:       "create",
		Status:       "accepted",
		CreatedAt:    timestamppb.New(now.Add(-time.Minute)),
	}}})
	emitRequest.Header().Set("Authorization", "Bearer "+token)
	emitResponse, err := client.Emit(context.Background(), emitRequest)
	require.NoError(t, err)
	assert.Equal(t, int32(1), emitResponse.Msg.GetAccepted())

	require.Eventually(t, func() bool {
		queryRequest := connect.NewRequest(&auditv1.QueryAuditEventsRequest{
			Filter: &auditv1.AuditQueryFilter{
				TimeRange: &commonv1.TimestampRange{
					Start: timestamppb.New(now.Add(-time.Hour)),
					End:   timestamppb.New(now),
				},
			},
		})
		queryRequest.Header().Set("Authorization", "Bearer "+token)
		queryResponse, queryErr := client.QueryAuditEvents(context.Background(), queryRequest)
		return queryErr == nil && len(queryResponse.Msg.GetEvents()) == 1
	}, 6*time.Second, 100*time.Millisecond)

	exportRequest := connect.NewRequest(&auditv1.ExportAuditEventsRequest{
		Filter: &auditv1.AuditQueryFilter{
			TimeRange: &commonv1.TimestampRange{
				Start: timestamppb.New(now.Add(-time.Hour)),
				End:   timestamppb.New(now),
			},
		},
	})
	exportRequest.Header().Set("Authorization", "Bearer "+token)
	exportResponse, err := client.ExportAuditEvents(context.Background(), exportRequest)
	require.NoError(t, err)
	assert.NotEmpty(t, exportResponse.Msg.GetExportId())
	assert.Equal(t, "pending", exportResponse.Msg.GetStatus())
}

func TestAPISvcCloseDrainsAuditEmitter(t *testing.T) {
	const signingKey = "test-signing-key"
	dbPath := t.TempDir() + "/api.db"
	mux := http.NewServeMux()
	svc := &apiSvc{dbPath: dbPath, signingKey: signingKey}
	require.NoError(t, svc.Register(mux, slog.Default()))

	result := svc.emitter.Emit(&store.AuditEvent{
		ID:             "event-close",
		ActorKind:      store.AuditActorSystem,
		OrganizationID: "org-001",
		ResourceType:   "api_service",
		ResourceID:     "release-api",
		Action:         "close",
		Status:         "accepted",
		Metadata:       map[string]string{},
	})
	require.True(t, result.Accepted)
	require.NoError(t, svc.Close(context.Background()))

	st, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	event, err := st.AuditEvents().GetByID(context.Background(), "event-close")
	require.NoError(t, err)
	assert.Equal(t, "org-001", event.OrganizationID)
}
