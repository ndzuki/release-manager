package orchestrator

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/internal/trust"
)

func setupService(t *testing.T) (*Service, store.Store, func()) {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	st, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	verifier := trust.NewStubVerifier(st.Verifications(), logger)
	svc := NewService(st, verifier, "staging", logger)

	return svc, st, func() { st.Close() }
}

func seedDefinition(t *testing.T, st store.Store) {
	t.Helper()

	// Ensure the customer exists (required for disabled checks).
	cust := &store.Customer{
		ID:   "cust-001",
		Name: "Test Customer",
		Slug: "test-customer",
	}
	require.NoError(t, st.Customers().Create(context.Background(), cust))

	def := &store.ReleaseDefinition{
		ID:          "def-001",
		Name:        "my-release",
		CustomerID:  "cust-001",
		ClusterID:   "cls-001",
		Namespace:   "default",
		ReleaseName: "my-release",
		ChartName:   "nginx",
		Status:      store.DefStatusActive,
		CreatedBy:   "test",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	err := st.Definitions().Create(context.Background(), def)
	require.NoError(t, err)

	revision := &store.ValuesRevision{
		ID:                  "vr-001",
		ReleaseDefinitionID: def.ID,
		Revision:            1,
		Status:              store.ValuesStatusApproved,
		Values:              []byte(`{"message":"hello"}`),
		Digest:              "digest-vr-001",
	}
	require.NoError(t, st.Values().Create(context.Background(), revision))
}

func seedValuesRevision(
	t *testing.T,
	st store.Store,
	id string,
	definitionID string,
	status store.ValuesStatus,
) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, st.Values().Create(context.Background(), &store.ValuesRevision{
		ID:                  id,
		ReleaseDefinitionID: definitionID,
		Revision:            1,
		Status:              status,
		Values:              []byte(`{"replicas":2}`),
		Digest:              "sha256:test",
		CreatedAt:           now,
		UpdatedAt:           now,
	}))
}

func upgradeRequest(valuesRevisionID string) *orchestratorv1.CreateOperationRequest {
	return &orchestratorv1.CreateOperationRequest{
		OperationType:           "UPGRADE",
		BundleId:                "bundle-upgrade",
		ReleaseDefinitionId:     "def-001",
		ValuesRevisionId:        valuesRevisionID,
		ExpectedCurrentRevision: 1,
		IdempotencyKey:          "idem-upgrade-" + valuesRevisionID,
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}
}

func TestCreateOperation_Install_Success(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	resp, err := svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:           "INSTALL",
		BundleId:                "bundle-001",
		ReleaseDefinitionId:     "def-001",
		ValuesRevisionId:        "vr-001",
		IdempotencyKey:          "idem-001",
		ExpectedCurrentRevision: 0,
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Msg.OperationId)
	assert.Equal(t, "preflight", resp.Msg.State) // standard ops enter preflight
	assert.NotNil(t, resp.Msg.AcceptedAt)
}

func TestCreateOperation_Idempotency(t *testing.T) {
	// AC-003-03: same idempotency_key returns original operation
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	msg := &orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
		IdempotencyKey:      "idem-dup",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}

	resp1, err := svc.CreateOperation(context.Background(), connect.NewRequest(msg))
	require.NoError(t, err)

	resp2, err := svc.CreateOperation(context.Background(), connect.NewRequest(msg))
	require.NoError(t, err)

	assert.Equal(t, resp1.Msg.OperationId, resp2.Msg.OperationId, "idempotent requests must return same operation")
}

func TestCreateOperation_ReleaseBusy(t *testing.T) {
	// AC-003-04: same definition, non-terminal operation -> release_busy
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	_, err := svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
		IdempotencyKey:      "idem-002",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}))
	require.NoError(t, err)

	// Second request with different idempotency key -> release_busy
	_, err = svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "UPGRADE",
		BundleId:            "bundle-002",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-002",
		IdempotencyKey:      "idem-003",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "release_busy")
}

func TestCreateOperation_UpgradeValidation(t *testing.T) {
	tests := []struct {
		name        string
		prepare     func(*testing.T, store.Store)
		mutate      func(*orchestratorv1.CreateOperationRequest)
		wantCode    connect.Code
		wantText    string
		wantCreated bool
	}{
		{
			name: "expected revision required",
			prepare: func(t *testing.T, st store.Store) {
				seedValuesRevision(t, st, "vr-approved", "def-001", store.ValuesStatusApproved)
			},
			mutate: func(req *orchestratorv1.CreateOperationRequest) {
				req.ExpectedCurrentRevision = 0
			},
			wantCode: connect.CodeInvalidArgument,
			wantText: "expected_current_revision",
		},
		{
			name:     "values revision must exist",
			wantCode: connect.CodeNotFound,
			wantText: "values_revision not found",
		},
		{
			name: "values revision must be approved",
			prepare: func(t *testing.T, st store.Store) {
				seedValuesRevision(t, st, "vr-draft", "def-001", store.ValuesStatusDraft)
			},
			wantCode: connect.CodeFailedPrecondition,
			wantText: "must be approved",
		},
		{
			name: "values revision must belong to definition",
			prepare: func(t *testing.T, st store.Store) {
				now := time.Now().UTC()
				require.NoError(t, st.Definitions().Create(context.Background(), &store.ReleaseDefinition{
					ID:          "def-other",
					Name:        "other-release",
					CustomerID:  "cust-001",
					ClusterID:   "cls-other",
					Namespace:   "other",
					ReleaseName: "other-release",
					ChartName:   "nginx",
					Status:      store.DefStatusActive,
					CreatedBy:   "test",
					CreatedAt:   now,
					UpdatedAt:   now,
				}))
				seedValuesRevision(t, st, "vr-other", "def-other", store.ValuesStatusApproved)
			},
			wantCode: connect.CodeInvalidArgument,
			wantText: "belongs to release_definition",
		},
		{
			name: "approved values creates operation",
			prepare: func(t *testing.T, st store.Store) {
				seedValuesRevision(t, st, "vr-approved", "def-001", store.ValuesStatusApproved)
			},
			wantCreated: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, st, cleanup := setupService(t)
			defer cleanup()
			seedDefinition(t, st)
			if tt.prepare != nil {
				tt.prepare(t, st)
			}

			valuesID := "vr-missing"
			switch tt.name {
			case "expected revision required", "approved values creates operation":
				valuesID = "vr-approved"
			case "values revision must be approved":
				valuesID = "vr-draft"
			case "values revision must belong to definition":
				valuesID = "vr-other"
			}
			req := upgradeRequest(valuesID)
			if tt.mutate != nil {
				tt.mutate(req)
			}

			resp, err := svc.CreateOperation(context.Background(), connect.NewRequest(req))
			if tt.wantCreated {
				require.NoError(t, err)
				assert.NotEmpty(t, resp.Msg.OperationId)
				stored, getErr := st.Operations().Get(context.Background(), resp.Msg.OperationId)
				require.NoError(t, getErr)
				assert.Equal(t, 1, stored.ExpectedRevision)
				assert.Equal(t, valuesID, stored.ValuesRevisionID)
				return
			}

			require.Error(t, err)
			assert.Equal(t, tt.wantCode, connect.CodeOf(err))
			assert.Contains(t, err.Error(), tt.wantText)
			operations, listErr := st.Operations().List(context.Background(), "def-001")
			require.NoError(t, listErr)
			assert.Empty(t, operations)
		})
	}
}

func TestCreateOperation_UpgradeDoesNotMutateOtherDefinition(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedValuesRevision(t, st, "vr-approved", "def-001", store.ValuesStatusApproved)

	other := &store.ReleaseDefinition{
		ID:          "def-other",
		Name:        "other-release",
		CustomerID:  "cust-001",
		ClusterID:   "cls-other",
		Namespace:   "other",
		ReleaseName: "other-release",
		ChartName:   "nginx",
		Status:      store.DefStatusActive,
		CreatedBy:   "test",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	require.NoError(t, st.Definitions().Create(context.Background(), other))

	resp, err := svc.CreateOperation(context.Background(), connect.NewRequest(upgradeRequest("vr-approved")))
	require.NoError(t, err)
	require.NotNil(t, resp)

	otherOperations, err := st.Operations().List(context.Background(), other.ID)
	require.NoError(t, err)
	assert.Empty(t, otherOperations)
}

func TestCreateOperation_DefinitionNotFound(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	_, err := svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		ReleaseDefinitionId: "nonexistent",
		IdempotencyKey:      "idem-004",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCreateOperation_InvalidType(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	_, err := svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INVALID",
		ReleaseDefinitionId: "def-001",
		IdempotencyKey:      "idem-005",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// REQ-012 AC-012-01: Digest mismatch → rejected, operation not created.
func TestCreateOperation_VerificationRejected_DigestMismatch(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	ctx := context.Background()
	_, err := svc.CreateOperation(ctx, connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
		IdempotencyKey:      "idem-verify-001",
		SignatureRef: &commonv1.SignatureRef{
			Digest:    "sha256:wrong",
			Signature: "MEUCIQD...",
			Issuer:    "evil-ci",
			Subject:   "release-manager/v1.0.0",
		},
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "artifact trust rejected")
}

// REQ-012: No signature_ref → verification skipped, operation created normally.
func TestCreateOperation_NoSignatureRef_SkipsVerification(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	ctx := context.Background()
	resp, err := svc.CreateOperation(ctx, connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
		IdempotencyKey:      "idem-verify-002",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Msg.OperationId)
	assert.Equal(t, commonv1.VerificationResult_VERIFICATION_RESULT_UNSPECIFIED, resp.Msg.VerificationResult)
}

func TestCreateOperation_InstallRequiresApprovedRevision(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	draft := &store.ValuesRevision{
		ID:                  "vr-draft",
		ReleaseDefinitionID: "def-001",
		Revision:            2,
		Status:              store.ValuesStatusDraft,
		Values:              []byte(`{}`),
		Digest:              "digest-vr-draft",
	}
	require.NoError(t, st.Values().Create(context.Background(), draft))

	_, err := svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    draft.ID,
		IdempotencyKey:      "idem-draft",
		Actor:               &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "revision_not_approved")
}
