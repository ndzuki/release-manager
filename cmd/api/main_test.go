package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
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
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// stubDecision allows every audit request and resolves the effective scope to the
// caller's own organization, which is what release-auth answers for a
// release_admin of that organization (ADR-021). The release-api test injects it
// instead of standing up release-auth.
type stubDecision struct {
	organization  string
	maxWindowDays int32
}

func (s stubDecision) Authorize(
	_ context.Context, _, _, _, _ string,
) (audit.AccessDecision, error) {
	return audit.AccessDecision{
		Allowed: true, Reason: "ok", OrganizationID: s.organization,
		MaxWindowDays: s.maxWindowDays, PolicyVersion: 3,
	}, nil
}

// apiTestJWTKeys is the Ed25519 management-plane key pair every cmd/api test
// uses (REQ-065 AC-065-01): the audit API is configured with the public half
// and the tests mint tokens with the private half.
var (
	apiTestJWTPublicKeyPEM string
	apiTestJWTPrivateKey   ed25519.PrivateKey
)

func init() {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic("generate test JWT key pair: " + err.Error())
	}
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		panic("marshal test JWT public key: " + err.Error())
	}
	apiTestJWTPublicKeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	apiTestJWTPrivateKey = privateKey
}

func TestAPISvcAuditConnectEndToEnd(t *testing.T) {
	dbPath := t.TempDir() + "/api.db"
	mux := http.NewServeMux()
	// A short flush interval keeps the audit wait deterministic: the production
	// default is 5s, which leaves under a second of slack in the Eventually budget.
	svc := &apiSvc{
		dbPath: dbPath, jwtPublicKey: apiTestJWTPublicKeyPEM, auditFlushInterval: 10 * time.Millisecond,
		decisionClient: stubDecision{organization: "org-001", maxWindowDays: 31},
	}
	require.NoError(t, svc.Register(mux, slog.Default()))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	jwtManager := auth.NewJWTManager(apiTestJWTPrivateKey, time.Hour, 24*time.Hour)
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

// REQ-065 AC-065-01: cmd/api verifies EdDSA tokens with a public key, and a
// missing or non-Ed25519 key must fail startup rather than leave the service
// accepting nothing (or, worse, something it cannot check).
func TestAPIRegisterFailsClosedOnNonEd25519Key(t *testing.T) {
	legacy := make([]byte, 64)
	_, err := rand.Read(legacy)
	require.NoError(t, err)

	svc := &apiSvc{dbPath: t.TempDir() + "/api.db", jwtPublicKey: base64.StdEncoding.EncodeToString(legacy)}

	err = svc.Register(http.NewServeMux(), slog.New(slog.DiscardHandler))
	require.Error(t, err, "the legacy symmetric key format must not configure a verifier")
	assert.Contains(t, err.Error(), "jwt verification key")
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
}

func TestAPISvcCloseDrainsAuditEmitter(t *testing.T) {
	dbPath := t.TempDir() + "/api.db"
	mux := http.NewServeMux()
	svc := &apiSvc{dbPath: dbPath, jwtPublicKey: apiTestJWTPublicKeyPEM}
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
	require.NoError(t, svc.Close())

	st, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	event, err := st.AuditEvents().GetByID(context.Background(), "event-close")
	require.NoError(t, err)
	assert.Equal(t, "org-001", event.OrganizationID)
}
