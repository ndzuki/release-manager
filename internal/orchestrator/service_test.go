package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/orchestrator/preflight"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/internal/trust"
	valueutil "github.com/ndzuki/release-manager/internal/values"
)

func setupService(t *testing.T) (*Service, store.Store, func()) {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	st, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	verifier := trust.NewStubVerifier(st.Verifications(), nil, logger)
	svc := NewService(st, st.OperationCreationUnitOfWork(), verifier, "staging", nil, logger)

	return svc, st, func() { st.Close() }
}

func seedDefinition(t *testing.T, st store.Store) {
	t.Helper()
	seedDefinitionFixture(t, st, false, 0)
}

func seedInstalledDefinition(t *testing.T, st store.Store, revision int) {
	t.Helper()
	seedDefinitionFixture(t, st, true, revision)
}

func seedDefinitionFixture(t *testing.T, st store.Store, installed bool, revisionNumber int) {
	t.Helper()

	// Ensure the customer exists (required for disabled checks).
	cust := &store.Customer{
		ID:   "cust-001",
		Name: "Test Customer",
		Slug: "test-customer",
	}
	require.NoError(t, st.Customers().Create(context.Background(), cust))
	require.NoError(t, st.Users().Create(context.Background(), &store.User{ID: "user-001", Username: "user-001", PasswordHash: "test"}))

	org := &store.Organization{ID: "org-001", Name: "Test Organization"}
	require.NoError(t, st.Organizations().Create(context.Background(), org))
	binding := &store.OrgCustomerBinding{
		ID:         "binding-001",
		OrgID:      org.ID,
		CustomerID: cust.ID,
	}
	require.NoError(t, st.Bindings().Create(context.Background(), binding))
	require.NoError(t, st.OrgMembers().Create(context.Background(), &store.OrganizationMember{
		OrgID: org.ID, UserID: "user-001", Role: store.RoleReleaseAdmin,
	}))

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

	// Seed a matching bundle for AC-067-03 chart_mismatch check.
	bundle := &store.ReleaseBundle{
		ID:           "bundle-001",
		Name:         "test-bundle",
		Status:       store.BundleValidated,
		ChartRef:     "nginx",
		ChartVersion: "1.0.0",
		ChartDigest:  "sha256:abc",
	}
	cluster := &store.Cluster{ID: "cls-001", Name: "test-cluster", CustomerID: cust.ID}
	require.NoError(t, st.Clusters().Create(context.Background(), cluster))
	operator := &store.Operator{ID: "operator-001", CustomerID: cust.ID, ClusterID: cluster.ID, CertSerial: "cert-001", Status: store.OperatorActive}
	require.NoError(t, st.Operators().Create(context.Background(), operator))
	require.NoError(t, st.Bundles().Create(context.Background(), bundle))
	err := st.Definitions().Create(context.Background(), def, nil)
	require.NoError(t, err)

	valuesRevision := &store.ValuesRevision{
		ID:                  "vr-001",
		ReleaseDefinitionID: def.ID,
		Revision:            1,
		Status:              store.ValuesStatusApproved,
		Values:              []byte(`{"message":"hello"}`),
		Digest:              "digest-vr-001",
	}
	require.NoError(t, st.Values().Create(context.Background(), valuesRevision))

	if !installed {
		return
	}
	installedRelease := &store.ReleaseInventory{
		ReleaseDefinitionID: def.ID,
		CustomerID:          cust.ID,
		ClusterID:           def.ClusterID,
		Namespace:           def.Namespace,
		ReleaseName:         def.ReleaseName,
		Chart:               "nginx",
		ChartVersion:        "1.0.0",
		Revision:            revisionNumber,
		Status:              "deployed",
		InventoryStatus:     store.InventoryActive,
	}
	require.NoError(t, st.Inventories().Upsert(context.Background(), installedRelease))
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
		BundleId:                "bundle-001",
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
	assert.Equal(t, "pending", resp.Msg.State)
	assert.NotNil(t, resp.Msg.AcceptedAt)
}

func TestCreateOperation_RejectedForRevokedCustomerBinding(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	binding, err := st.Bindings().GetByOrgAndCustomer(context.Background(), "org-001", "cust-001")
	require.NoError(t, err)
	require.NoError(t, st.Bindings().SetStatus(context.Background(), binding.ID, store.BindingRevoked))

	_, err = svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
		IdempotencyKey:      "revoked-binding",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.ErrorContains(t, err, "permission_denied")
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

func TestCreateOperation_IdempotencyConflict(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	first := &orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
		IdempotencyKey:      "idem-conflict",
		Actor:               &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}
	_, err := svc.CreateOperation(context.Background(), connect.NewRequest(first))
	require.NoError(t, err)

	conflicting := &orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-002",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
		IdempotencyKey:      "idem-conflict",
		Actor:               &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}
	_, err = svc.CreateOperation(context.Background(), connect.NewRequest(conflicting))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	assert.ErrorContains(t, err, "idempotency_conflict")
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

	seedValuesRevision(t, st, "vr-002", "def-001", store.ValuesStatusApproved)

	// Second request with different idempotency key -> release_busy
	_, err = svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:           "UPGRADE",
		BundleId:                "bundle-002",
		ReleaseDefinitionId:     "def-001",
		ValuesRevisionId:        "vr-002",
		ExpectedCurrentRevision: 1,
		IdempotencyKey:          "idem-003",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "release_busy")
}

func TestCreateOperation_ConcurrentUpgradeOnlyOneAccepted(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedInstalledDefinition(t, st, 1)
	seedValuesRevision(t, st, "vr-concurrent", "def-001", store.ValuesStatusApproved)

	const requests = 8
	results := make(chan error, requests)
	for i := range requests {
		go func(i int) {
			req := upgradeRequest("vr-concurrent")
			req.IdempotencyKey = fmt.Sprintf("idem-concurrent-%d", i)
			_, err := svc.CreateOperation(context.Background(), connect.NewRequest(req))
			results <- err
		}(i)
	}

	accepted := 0
	busy := 0
	for range requests {
		err := <-results
		switch {
		case err == nil:
			accepted++
		case connect.CodeOf(err) == connect.CodeFailedPrecondition && strings.Contains(err.Error(), "release_busy"):
			busy++
		default:
			t.Logf("unexpected error: %v", err)
		}
	}
	t.Logf("concurrent upgrade: accepted=%d busy=%d", accepted, busy)

	// At least 1 must be accepted; at most 1 operation is non-terminal.
	assert.GreaterOrEqual(t, accepted, 1, "at least one concurrent request must be accepted")

	ops, err := st.Operations().List(context.Background(), "def-001")
	require.NoError(t, err)

	nonTerminal := 0
	for _, op := range ops {
		if !op.Status.IsTerminal() {
			nonTerminal++
		}
	}
	assert.Equal(t, 1, nonTerminal, "only one concurrent operation can be non-terminal")
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
				}, nil))
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
			seedInstalledDefinition(t, st, 1)
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
	seedInstalledDefinition(t, st, 1)
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
	require.NoError(t, st.Definitions().Create(context.Background(), other, nil))

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
		ValuesRevisionId:    "vr-001",
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

func TestCreateOperation_InstallWithNoInventorySucceeds(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	resp, err := svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001", IdempotencyKey: "idem-install-no-inventory",
		Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Msg.OperationId)
}

func TestCreateOperation_InstallRejectsExistingInventory(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedInstalledDefinition(t, st, 1)

	_, err := svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001", IdempotencyKey: "idem-install-existing",
		Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.ErrorContains(t, err, "release_already_exists")
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

// AC-067-02: UPGRADE with mismatched expected revision → revision_conflict, no write.
func TestCreateOperation_RevisionConflict(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedInstalledDefinition(t, st, 1)
	seedValuesRevision(t, st, "vr-approved", "def-001", store.ValuesStatusApproved)

	// Installed release has revision 1 (seeded by seedDefinition).
	// Request expects revision 5 → conflict.
	_, err := svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:           "UPGRADE",
		BundleId:                "bundle-001",
		ReleaseDefinitionId:     "def-001",
		ValuesRevisionId:        "vr-approved",
		ExpectedCurrentRevision: 5,
		IdempotencyKey:          "idem-conflict-rev",
		Actor:                   &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "revision_conflict")

	// Verify no operation was created.
	ops, listErr := st.Operations().List(context.Background(), "def-001")
	require.NoError(t, listErr)
	assert.Empty(t, ops)
}

// AC-067-02: UPGRADE when no installed release → release_not_found.
func TestCreateOperation_UpgradeNoInstalledRelease(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedValuesRevision(t, st, "vr-approved", "def-001", store.ValuesStatusApproved)

	// Delete the installed inventory to simulate no release installed.
	// Use a different definition that has no inventory.
	otherDef := &store.ReleaseDefinition{
		ID:          "def-no-inv",
		Name:        "no-inventory-release",
		CustomerID:  "cust-001",
		ClusterID:   "cls-001",
		Namespace:   "default",
		ReleaseName: "no-inv",
		ChartName:   "nginx",
		Status:      store.DefStatusActive,
		CreatedBy:   "test",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	require.NoError(t, st.Definitions().Create(context.Background(), otherDef, nil))
	seedValuesRevision(t, st, "vr-noinv", otherDef.ID, store.ValuesStatusApproved)
	// Seed bundle for the other def's chart check.
	bundle2 := &store.ReleaseBundle{
		ID: "bundle-002", Name: "bundle-2", Status: store.BundleValidated,
		ChartRef: "nginx", ChartVersion: "1.0.0",
	}
	require.NoError(t, st.Bundles().Create(context.Background(), bundle2))

	_, err := svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:           "UPGRADE",
		BundleId:                "bundle-002",
		ReleaseDefinitionId:     otherDef.ID,
		ValuesRevisionId:        "vr-noinv",
		ExpectedCurrentRevision: 1,
		IdempotencyKey:          "idem-no-inv",
		Actor:                   &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "release_not_found")
}

// AC-067-03: Bundle chart_ref doesn't match definition chart_name → chart_mismatch.
func TestCreateOperation_ChartMismatch(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	// Create a bundle with a different chart.
	mismatchBundle := &store.ReleaseBundle{
		ID: "bundle-mismatch", Name: "wrong-chart", Status: store.BundleValidated,
		ChartRef: "redis", ChartVersion: "1.0.0",
	}
	require.NoError(t, st.Bundles().Create(context.Background(), mismatchBundle))

	_, err := svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-mismatch",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
		IdempotencyKey:      "idem-chart-mismatch",
		Actor:               &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "chart_mismatch")

	// Verify no operation created.
	ops, listErr := st.Operations().List(context.Background(), "def-001")
	require.NoError(t, listErr)
	assert.Empty(t, ops)
}

// AC-067-03: Bundle with registry-qualified chart_ref matches correctly.
func TestCreateOperation_ChartMatchRegistryPrefix(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	// Bundle chart_ref has registry prefix, should still match "nginx".
	regBundle := &store.ReleaseBundle{
		ID: "bundle-reg", Name: "reg-bundle", Status: store.BundleValidated,
		ChartRef: "registry.example.com/nginx", ChartVersion: "1.0.0",
	}
	require.NoError(t, st.Bundles().Create(context.Background(), regBundle))

	resp, err := svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-reg",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
		IdempotencyKey:      "idem-reg-prefix",
		Actor:               &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Msg.OperationId)
}

// AC-067-05: Cross-organization actor → permission_denied.
func TestCreateOperation_CrossOrgPermissionDenied(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	// Create a second organization with NO binding to cust-001.
	otherOrg := &store.Organization{ID: "org-other", Name: "Other Organization"}
	require.NoError(t, st.Organizations().Create(context.Background(), otherOrg))

	_, err := svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
		IdempotencyKey:      "idem-cross-org",
		Actor:               &commonv1.ActorContext{UserId: "user-other", Organization: "org-other"},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "permission_denied")

	// Verify no operation created.
	ops, listErr := st.Operations().List(context.Background(), "def-001")
	require.NoError(t, listErr)
	assert.Empty(t, ops)
}

func TestCreateOperation_BundleStatusGate(t *testing.T) {
	tests := []struct {
		name       string
		status     store.BundleStatus
		wantCode   connect.Code
		wantReason string
	}{
		{name: "received bundle", status: store.BundleReceived, wantCode: connect.CodeFailedPrecondition, wantReason: "bundle_not_ready"},
		{name: "rejected bundle", status: store.BundleRejected, wantCode: connect.CodeFailedPrecondition, wantReason: "bundle_rejected"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, st, cleanup := setupService(t)
			defer cleanup()
			seedDefinition(t, st)

			bundleID := "bundle-" + string(tt.status)
			require.NoError(t, st.Bundles().Create(context.Background(), &store.ReleaseBundle{
				ID: bundleID, Name: tt.name, Status: tt.status, ChartRef: "nginx",
			}))

			_, err := svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
				OperationType: "UPGRADE", BundleId: bundleID, ReleaseDefinitionId: "def-001",
				ValuesRevisionId: "vr-001", ExpectedCurrentRevision: 1,
				IdempotencyKey: "idem-" + string(tt.status),
				Actor:          &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
			}))
			require.Error(t, err)
			assert.Equal(t, tt.wantCode, connect.CodeOf(err))
			assert.ErrorContains(t, err, tt.wantReason)
		})
	}
}

func TestCreateOperation_ValuesPatchPersistsCanonicalDigests(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	resp, err := svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001", ValuesPatch: `{"replicas":2}`,
		IdempotencyKey: "idem-values-patch", Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.NoError(t, err)
	op, err := st.Operations().Get(context.Background(), resp.Msg.OperationId)
	require.NoError(t, err)
	assert.Equal(t, `{"replicas":2}`, string(op.ValuesPatch))
	assert.NotEmpty(t, op.PatchDigest)
	assert.NotEmpty(t, op.EffectiveValuesDigest)
}

func TestCreateOperation_ValuesPatchMergeSemantics(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	resp, err := svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001", ValuesPatch: `{"image":{"tag":"2.0"},"replicas":null,"tolerations":[{"key":"new"}]}`,
		IdempotencyKey: "idem-values-merge", Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.NoError(t, err)
	op, err := st.Operations().Get(t.Context(), resp.Msg.OperationId)
	require.NoError(t, err)
	assert.Equal(t, valueutil.Digest([]byte(`{"image":{"tag":"2.0"},"message":"hello","tolerations":[{"key":"new"}]}`)), op.EffectiveValuesDigest)
}

func TestCreateOperation_InvalidMergePatchRejected(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	_, err := svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001", ValuesPatch: `[]`, IdempotencyKey: "idem-invalid-patch",
		Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.ErrorContains(t, err, "invalid_merge_patch")
	operations, listErr := st.Operations().List(t.Context(), "def-001")
	require.NoError(t, listErr)
	assert.Empty(t, operations)
}

func TestCreateOperation_ValuesPatchSecretRejected(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	_, err := svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001", ValuesPatch: `{"password":"literal-secret"}`,
		IdempotencyKey: "idem-secret-patch", Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.ErrorContains(t, err, "secret_literal_forbidden")
}

func TestCreateOperation_RoleGate(t *testing.T) {
	tests := []struct {
		name     string
		role     store.Role
		wantCode connect.Code
	}{
		{name: "release admin allowed", role: store.RoleReleaseAdmin},
		{name: "platform admin allowed", role: store.RolePlatformAdmin},
		{name: "deployer denied", role: store.RoleDeployer, wantCode: connect.CodePermissionDenied},
		{name: "viewer denied", role: store.RoleViewer, wantCode: connect.CodePermissionDenied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, st, cleanup := setupService(t)
			defer cleanup()
			seedDefinition(t, st)
			member, err := st.OrgMembers().Get(t.Context(), "org-001", "user-001")
			require.NoError(t, err)
			member.Role = tt.role
			require.NoError(t, st.OrgMembers().Update(t.Context(), member))

			resp, err := svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
				OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
				ValuesRevisionId: "vr-001", IdempotencyKey: "idem-role-" + string(tt.role),
				Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
			}))
			if tt.wantCode == 0 {
				require.NoError(t, err)
				assert.NotEmpty(t, resp.Msg.OperationId)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.wantCode, connect.CodeOf(err))
			assert.ErrorContains(t, err, "permission_denied")
		})
	}
}

func TestCreateOperation_IdempotencyScopeIsolation(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	first, err := svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001", IdempotencyKey: "shared-key",
		Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.NoError(t, err)

	secondScope := idempotencyScope("org-other", "def-001")
	second := &store.Operation{
		ID: "op-other-scope", OperationType: store.OperationInstall, Status: store.StatusSucceeded,
		ReleaseDefinitionID: "def-001", IdempotencyKey: "shared-key", IdempotencyScope: secondScope,
		RequestHash: "other-request", Actor: store.ActorContext{UserID: "other", Organization: "org-other"},
	}
	require.NoError(t, st.Operations().Create(t.Context(), second))

	storedFirst, err := st.Operations().GetByIdempotencyScopeAndKey(t.Context(), idempotencyScope("org-001", "def-001"), "shared-key")
	require.NoError(t, err)
	storedSecond, err := st.Operations().GetByIdempotencyScopeAndKey(t.Context(), secondScope, "shared-key")
	require.NoError(t, err)
	assert.Equal(t, first.Msg.OperationId, storedFirst.ID)
	assert.Equal(t, second.ID, storedSecond.ID)
}

func TestCreateOperation_CanonicalPatchIdempotency(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	request := func(patch string) *orchestratorv1.CreateOperationRequest {
		return &orchestratorv1.CreateOperationRequest{
			OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
			ValuesRevisionId: "vr-001", ValuesPatch: patch, IdempotencyKey: "canonical-patch",
			Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
		}
	}
	first, err := svc.CreateOperation(t.Context(), connect.NewRequest(request(`{"b":2,"a":1}`)))
	require.NoError(t, err)
	second, err := svc.CreateOperation(t.Context(), connect.NewRequest(request("{\n  \"a\": 1,\n  \"b\": 2\n}")))
	require.NoError(t, err)
	assert.Equal(t, first.Msg.OperationId, second.Msg.OperationId)
}

func TestCreateOperation_DispatchRecordPersists(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	resp, err := svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001", IdempotencyKey: "dispatch-persisted",
		Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.NoError(t, err)
	assert.Equal(t, "pending", resp.Msg.State)

	entry, err := st.Outbox().GetByCommandID(t.Context(), resp.Msg.OperationId+":artifact")
	require.NoError(t, err)
	assert.Equal(t, store.CommandPending, entry.Status)
	payload, err := preflight.UnmarshalCommandPayload(entry.Payload)
	require.NoError(t, err)
	assert.Equal(t, "def-001", payload.DefinitionID)
	assert.Equal(t, "default", payload.Namespace)
	assert.Equal(t, "my-release", payload.ReleaseName)
	stored, err := st.Operations().Get(t.Context(), resp.Msg.OperationId)
	require.NoError(t, err)
	assert.Equal(t, store.StatusPending, stored.Status)
}

func TestCreateOperation_CoordinatorUnavailableStillPersistsPending(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	now := time.Now().UTC()
	require.NoError(t, st.Definitions().Create(t.Context(), &store.ReleaseDefinition{
		ID: "def-no-operator", Name: "no-operator", CustomerID: "cust-001", ClusterID: "cluster-no-operator",
		Namespace: "default", ReleaseName: "no-operator", ChartName: "nginx", Status: store.DefStatusActive,
		CreatedBy: "test", CreatedAt: now, UpdatedAt: now,
	}, nil))
	seedValuesRevision(t, st, "vr-no-operator", "def-no-operator", store.ValuesStatusApproved)

	resp, err := svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-no-operator",
		ValuesRevisionId: "vr-no-operator", IdempotencyKey: "dispatch-deferred",
		Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.NoError(t, err)
	assert.Equal(t, "pending", resp.Msg.State)
	stored, err := st.Operations().Get(t.Context(), resp.Msg.OperationId)
	require.NoError(t, err)
	assert.Equal(t, store.StatusPending, stored.Status)
}

func TestCreateOperation_CoordinatorUnavailablePersistsDeferredDispatch(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	now := time.Now().UTC()
	require.NoError(t, st.Definitions().Create(t.Context(), &store.ReleaseDefinition{
		ID: "def-deferred-dispatch", Name: "deferred-dispatch", CustomerID: "cust-001", ClusterID: "cluster-no-operator",
		Namespace: "default", ReleaseName: "deferred-dispatch", ChartName: "nginx", Status: store.DefStatusActive,
		CreatedBy: "test", CreatedAt: now, UpdatedAt: now,
	}, nil))
	seedValuesRevision(t, st, "vr-deferred-dispatch", "def-deferred-dispatch", store.ValuesStatusApproved)

	resp, err := svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-deferred-dispatch",
		ValuesRevisionId: "vr-deferred-dispatch", IdempotencyKey: "dispatch-deferred-persisted",
		Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.NoError(t, err)
	assert.Equal(t, "pending", resp.Msg.State)
	entry, err := st.Outbox().GetByCommandID(t.Context(), resp.Msg.OperationId+":artifact")
	require.NoError(t, err)
	assert.Empty(t, entry.OperatorID)
	assert.Equal(t, store.CommandPending, entry.Status)
}

func TestCreateOperation_ArchivedValidatedBundleRestored(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	archived, err := st.Bundles().Archive(t.Context(), []string{"bundle-001"})
	require.NoError(t, err)
	require.Equal(t, int64(1), archived)

	resp, err := svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001", IdempotencyKey: "archived-validated",
		Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.NoError(t, err)
	assert.Equal(t, "pending", resp.Msg.State)
	assert.NotEmpty(t, resp.Msg.OperationId)

	bundle, err := st.Bundles().Get(t.Context(), "bundle-001")
	require.NoError(t, err)
	assert.Equal(t, store.BundleValidated, bundle.Status)
	assert.Nil(t, bundle.ArchivedAt)
	assert.Empty(t, bundle.ArchivedFromStatus)

	definition, err := st.Definitions().Get(t.Context(), "def-001")
	require.NoError(t, err)
	require.NotNil(t, definition.CurrentBundleID)
	assert.Equal(t, "bundle-001", *definition.CurrentBundleID)
}

func TestCreateOperation_ArchivedReceivedBundleRejected(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	const bundleID = "bundle-archived-received"
	require.NoError(t, st.Bundles().Create(t.Context(), &store.ReleaseBundle{
		ID: bundleID, Name: "archived-received", Status: store.BundleReceived, ChartRef: "nginx",
	}))
	archived, err := st.Bundles().Archive(t.Context(), []string{bundleID})
	require.NoError(t, err)
	require.Equal(t, int64(1), archived)

	_, err = svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: bundleID, ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001", IdempotencyKey: "archived-received",
		Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.ErrorContains(t, err, "bundle_not_ready")

	operations, listErr := st.Operations().List(t.Context(), "def-001")
	require.NoError(t, listErr)
	assert.Empty(t, operations)
	definition, getErr := st.Definitions().Get(t.Context(), "def-001")
	require.NoError(t, getErr)
	assert.Nil(t, definition.CurrentBundleID)
	bundle, getErr := st.Bundles().Get(t.Context(), bundleID)
	require.NoError(t, getErr)
	assert.Equal(t, store.BundleArchived, bundle.Status)
	assert.Equal(t, store.BundleReceived, bundle.ArchivedFromStatus)
}

func TestCreateOperation_UnitOfWorkAtomicCommit(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	const bundleID = "bundle-uow-handler"
	bundle := &store.ReleaseBundle{
		ID: bundleID, Name: "uow-handler", Status: store.BundleValidated, ChartRef: "nginx",
		ChartDigest: "sha256:uow-chart",
		Images:      []store.BundleImage{{Ref: "registry.example.com/app:v1", Digest: "sha256:uow-image"}},
	}
	require.NoError(t, st.Bundles().Create(t.Context(), bundle))
	for artifactType, digest := range map[store.ArtifactType]string{
		store.ArtifactChart: bundle.ChartDigest,
		store.ArtifactImage: bundle.Images[0].Digest,
	} {
		require.NoError(t, st.CandidateArtifacts().Create(t.Context(), &store.CandidateArtifact{
			ArtifactType: artifactType, Ref: string(artifactType), Digest: digest,
		}))
	}

	resp, err := svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: bundleID, ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001", IdempotencyKey: "uow-atomic-commit",
		Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))
	require.NoError(t, err)

	operationRecord, err := st.Operations().Get(t.Context(), resp.Msg.OperationId)
	require.NoError(t, err)
	assert.Equal(t, bundleID, operationRecord.BundleID)
	_, err = st.Outbox().GetByCommandID(t.Context(), resp.Msg.OperationId+":artifact")
	require.NoError(t, err)
	definition, err := st.Definitions().Get(t.Context(), "def-001")
	require.NoError(t, err)
	require.NotNil(t, definition.CurrentBundleID)
	assert.Equal(t, bundleID, *definition.CurrentBundleID)

	sqliteStore, ok := st.(*sqlitestore.Store)
	require.True(t, ok)
	var linked int
	require.NoError(t, sqliteStore.DB().QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM bundle_candidate_artifacts WHERE bundle_id = ?
	`, bundleID).Scan(&linked))
	assert.Equal(t, 2, linked)
	var claimed int
	require.NoError(t, sqliteStore.DB().QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM candidate_artifacts
		WHERE digest IN (?, ?) AND orphaned_at IS NULL
	`, bundle.ChartDigest, bundle.Images[0].Digest).Scan(&claimed))
	assert.Equal(t, 2, claimed)
}
