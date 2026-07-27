package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/internal/trust"
)

type streamRevokerStub struct {
	operatorID string
	reason     string
	calls      int
	err        error
}

func (r *streamRevokerStub) Revoke(_ context.Context, operatorID, reason string) error {
	r.operatorID = operatorID
	r.reason = reason
	r.calls++
	return r.err
}

func setupService(t *testing.T) (*Service, store.Store, func()) {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	st, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	verifier := trust.NewStubVerifier(st.Verifications(), nil, logger)
	svc := NewService(st, verifier, "staging", nil, logger)

	return svc, st, func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(shutdownCtx))
		require.NoError(t, st.Close())
	}
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

	org := &store.Organization{ID: "org-001", Name: "Test Organization"}
	require.NoError(t, st.Organizations().Create(context.Background(), org))
	binding := &store.OrgCustomerBinding{
		ID:         "binding-001",
		OrgID:      org.ID,
		CustomerID: cust.ID,
	}
	require.NoError(t, st.Bindings().Create(context.Background(), binding))

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
	err := st.Definitions().Create(context.Background(), def, nil)
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
	initialStatus := status
	if status == store.ValuesStatusApproved {
		initialStatus = store.ValuesStatusPendingApproval
	}
	revision := &store.ValuesRevision{
		ID:                  id,
		ReleaseDefinitionID: definitionID,
		Revision:            1,
		StateVersion:        1,
		Status:              initialStatus,
		Values:              []byte(`{"replicas":2}`),
		Digest:              "sha256:test",
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	require.NoError(t, st.Values().Create(context.Background(), revision))
	if status == store.ValuesStatusApproved {
		_, err := st.ValuesApproval().Approve(context.Background(), store.ValuesApprovalCommand{
			RevisionID: id, ExpectedStateVersion: 1, ActorUserID: "test-approver", Authorized: true,
		})
		require.NoError(t, err)
	}
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
	assert.ErrorContains(t, err, "customer binding is not active")
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

	require.NoError(t, st.Operations().Create(context.Background(), &store.Operation{
		ID:                  "op-busy",
		OperationType:       store.OperationInstall,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: "def-001",
		IdempotencyKey:      "idem-002",
	}))

	seedValuesRevision(t, st, "vr-002", "def-001", store.ValuesStatusApproved)

	// Second request with different idempotency key -> release_busy
	_, err := svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
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
	seedDefinition(t, st)
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

func operatorSummaryIDs(operators []*orchestratorv1.OperatorSummary) []string {
	ids := make([]string, 0, len(operators))
	for _, operator := range operators {
		ids = append(ids, operator.GetId())
	}
	return ids
}

func TestOperatorManagementContracts(t *testing.T) {
	_, st, cleanup := setupService(t)
	defer cleanup()
	ctx := context.Background()
	revoker := &streamRevokerStub{}
	svc := NewService(st, trust.NewStubVerifier(st.Verifications(), nil, slog.New(slog.DiscardHandler)), "staging", revoker, slog.New(slog.DiscardHandler))
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: "org-operator", Name: "Operator Org"}))
	require.NoError(t, st.Users().Create(ctx, &store.User{ID: "user-operator", Username: "operator-admin", PasswordHash: "unused"}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: "org-operator", UserID: "user-operator", Role: store.RoleReleaseAdmin}))
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: "customer-operator", Name: "Operator Customer", Slug: "operator-customer"}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: "cluster-operator", Name: "Operator Cluster", CustomerID: "customer-operator"}))
	actorCtx := auth.ContextWithActor(ctx, auth.Actor{UserID: "user-operator", OrganizationID: "org-operator", Roles: []string{string(store.RoleReleaseAdmin)}})

	t.Run("AC-053-06 AC-053-07 AC-053-19 pending token conflict replacement and redacted audit", func(t *testing.T) {
		first, err := svc.CreateEnrollmentToken(actorCtx, connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
			CustomerId: "customer-operator", ClusterId: "cluster-operator", OperatorName: "operator-one", TtlMinutes: 5,
		}))
		require.NoError(t, err)
		assert.NotEmpty(t, first.Msg.GetToken())

		_, err = svc.CreateEnrollmentToken(actorCtx, connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
			CustomerId: "customer-operator", ClusterId: "cluster-operator", OperatorName: "operator-one", TtlMinutes: 5,
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))

		status, err := svc.GetEnrollmentTokenStatus(actorCtx, connect.NewRequest(&orchestratorv1.GetEnrollmentTokenStatusRequest{
			CustomerId: "customer-operator", ClusterId: "cluster-operator",
		}))
		require.NoError(t, err)
		assert.Equal(t, orchestratorv1.EnrollmentTokenState_ENROLLMENT_TOKEN_STATE_PENDING, status.Msg.GetStatus().GetState())
		assert.Equal(t, "operator-admin", status.Msg.GetStatus().GetCreatedByDisplayName())

		replacement, err := svc.CreateEnrollmentToken(actorCtx, connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
			CustomerId: "customer-operator", ClusterId: "cluster-operator", OperatorName: "operator-one", TtlMinutes: 5, ReplacePendingToken: true,
		}))
		require.NoError(t, err)
		assert.NotEqual(t, first.Msg.GetToken(), replacement.Msg.GetToken())

		createdAudit, err := st.AuditEvents().Query(ctx, store.AuditEventFilter{Action: "operator.enrollment_token.created"}, "", 20)
		require.NoError(t, err)
		require.Len(t, createdAudit.Events, 1)
		assert.NotContains(t, fmt.Sprint(createdAudit.Events[0].Metadata), first.Msg.GetToken())
		assert.Equal(t, store.AuditActorUser, createdAudit.Events[0].ActorKind)
		assert.Equal(t, "user-operator", createdAudit.Events[0].ActorID)
		assert.Equal(t, "org-operator", createdAudit.Events[0].OrganizationID)
		assert.Equal(t, "customer-operator", createdAudit.Events[0].Metadata["customer_id"])
		assert.Equal(t, "cluster-operator", createdAudit.Events[0].Metadata["cluster_id"])
		assert.NotEmpty(t, createdAudit.Events[0].ResourceID)
		assert.Equal(t, "succeeded", createdAudit.Events[0].Status)
		assert.NotEmpty(t, createdAudit.Events[0].Metadata["request_id"])
		replacedAudit, err := st.AuditEvents().Query(ctx, store.AuditEventFilter{Action: "operator.enrollment_token.replaced"}, "", 20)
		require.NoError(t, err)
		require.Len(t, replacedAudit.Events, 1)
		assert.NotContains(t, fmt.Sprint(replacedAudit.Events[0].Metadata), replacement.Msg.GetToken())
		assert.NotContains(t, fmt.Sprint(replacedAudit.Events[0].Metadata), first.Msg.GetToken())
		assert.Equal(t, store.AuditActorUser, replacedAudit.Events[0].ActorKind)
		assert.Equal(t, "user-operator", replacedAudit.Events[0].ActorID)
		assert.Equal(t, "org-operator", replacedAudit.Events[0].OrganizationID)
		assert.Equal(t, "customer-operator", replacedAudit.Events[0].Metadata["customer_id"])
		assert.Equal(t, "cluster-operator", replacedAudit.Events[0].Metadata["cluster_id"])
		assert.NotEmpty(t, replacedAudit.Events[0].ResourceID)
		assert.Equal(t, "succeeded", replacedAudit.Events[0].Status)
		assert.NotEmpty(t, replacedAudit.Events[0].Metadata["request_id"])
	})
	t.Run("AC-053-04 AC-053-13 AC-053-14 AC-053-18 AC-053-19 AC-053-22 revoke is scoped audited and idempotent", func(t *testing.T) {
		op := &store.Operator{ID: "operator-managed", Name: "operator-managed", CustomerID: "customer-operator", ClusterID: "cluster-operator", CertSerial: "serial-managed"}
		require.NoError(t, st.Operators().Create(ctx, op))
		require.NoError(t, st.Sessions().Create(ctx, &store.Session{ID: "session-managed", OperatorID: op.ID, CustomerID: op.CustomerID, ClusterID: op.ClusterID, Status: store.SessionOnline, LastHeartbeat: time.Now().UTC()}))

		_, err := svc.GetOperator(actorCtx, connect.NewRequest(&orchestratorv1.GetOperatorRequest{
			CustomerId: "customer-operator", ClusterId: "other-cluster", OperatorId: op.ID,
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

		for _, invalidReason := range []string{"four", strings.Repeat("界", 501)} {
			_, err := svc.RevokeOperator(actorCtx, connect.NewRequest(&orchestratorv1.RevokeOperatorRequest{
				CustomerId: "customer-operator", ClusterId: "cluster-operator", OperatorId: op.ID, Reason: invalidReason,
			}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		}

		first, err := svc.RevokeOperator(actorCtx, connect.NewRequest(&orchestratorv1.RevokeOperatorRequest{
			CustomerId: "customer-operator", ClusterId: "cluster-operator", OperatorId: op.ID, Reason: "security incident",
		}))
		require.NoError(t, err)
		assert.True(t, first.Msg.GetChanged())
		assert.Equal(t, orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_REVOKED, first.Msg.GetOperator().GetSessionStatus())
		assert.Equal(t, op.ID, revoker.operatorID)

		events, err := st.AuditEvents().ListByResource(ctx, "operator", op.ID)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, "operator.revoked", events[0].Action)
		assert.NotEmpty(t, events[0].Metadata["request_id"])
		assert.Equal(t, "true", events[0].Metadata["reason_present"])
		assert.Equal(t, fmt.Sprintf("%d", len([]rune("security incident"))), events[0].Metadata["reason_length"])
		assert.NotContains(t, fmt.Sprint(events[0].Metadata), "security incident")
		assert.Equal(t, store.AuditActorUser, events[0].ActorKind)
		assert.Equal(t, "user-operator", events[0].ActorID)
		assert.Equal(t, "org-operator", events[0].OrganizationID)
		assert.Equal(t, "customer-operator", events[0].Metadata["customer_id"])
		assert.Equal(t, "cluster-operator", events[0].Metadata["cluster_id"])
		assert.Equal(t, op.ID, events[0].ResourceID)
		assert.Equal(t, "succeeded", events[0].Status)

		second, err := svc.RevokeOperator(actorCtx, connect.NewRequest(&orchestratorv1.RevokeOperatorRequest{
			CustomerId: "customer-operator", ClusterId: "cluster-operator", OperatorId: op.ID, Reason: "must not overwrite",
		}))
		require.NoError(t, err)
		assert.False(t, second.Msg.GetChanged())
		detail, err := svc.GetOperator(actorCtx, connect.NewRequest(&orchestratorv1.GetOperatorRequest{
			CustomerId: "customer-operator", ClusterId: "cluster-operator", OperatorId: op.ID,
		}))
		require.NoError(t, err)
		assert.Equal(t, "security incident", detail.Msg.GetOperator().GetRevokeReason())
		events, err = st.AuditEvents().ListByResource(ctx, "operator", op.ID)
		require.NoError(t, err)
		require.Len(t, events, 2)
		assert.Equal(t, "operator.revoked", events[1].Action)
	})

	t.Run("AC-053-21 and AC-053-22 retry closes a stream after the committed revoke response is lost", func(t *testing.T) {
		op := &store.Operator{ID: "operator-retry-revoke", Name: "operator-retry-revoke", CustomerID: "customer-operator", ClusterID: "cluster-operator", CertSerial: "serial-retry-revoke"}
		require.NoError(t, st.Operators().Create(ctx, op))
		require.NoError(t, st.Sessions().Create(ctx, &store.Session{ID: "session-retry-revoke", OperatorID: op.ID, CustomerID: op.CustomerID, ClusterID: op.ClusterID, Status: store.SessionOnline, LastHeartbeat: time.Now().UTC()}))
		flakyRevoker := &streamRevokerStub{err: errors.New("operator service unavailable")}
		flakyService := NewService(st, trust.NewStubVerifier(st.Verifications(), nil, slog.New(slog.DiscardHandler)), "staging", flakyRevoker, slog.New(slog.DiscardHandler))

		_, err := flakyService.RevokeOperator(actorCtx, connect.NewRequest(&orchestratorv1.RevokeOperatorRequest{
			CustomerId: "customer-operator", ClusterId: "cluster-operator", OperatorId: op.ID, Reason: "security incident",
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
		persistedSession, err := st.Sessions().Get(ctx, "session-retry-revoke")
		require.NoError(t, err)
		assert.Equal(t, store.SessionRevoked, persistedSession.Status)

		flakyRevoker.err = nil
		retried, err := flakyService.RevokeOperator(actorCtx, connect.NewRequest(&orchestratorv1.RevokeOperatorRequest{
			CustomerId: "customer-operator", ClusterId: "cluster-operator", OperatorId: op.ID, Reason: "manual retry reason",
		}))
		require.NoError(t, err)
		assert.False(t, retried.Msg.GetChanged())
		assert.Equal(t, 2, flakyRevoker.calls)
	})

	t.Run("AC-053-02 AC-053-08 AC-053-09 AC-053-10 AC-053-12 AC-053-15 AC-053-17 status validation list and revoke pending", func(t *testing.T) {
		for _, test := range []struct {
			name     string
			ttl      int32
			wantCode connect.Code
		}{
			{name: "default ttl", ttl: 0},
			{name: "minimum ttl", ttl: 5},
			{name: "maximum ttl", ttl: 1440},
			{name: "below minimum", ttl: 4, wantCode: connect.CodeInvalidArgument},
			{name: "above maximum", ttl: 1441, wantCode: connect.CodeInvalidArgument},
		} {
			t.Run(test.name, func(t *testing.T) {
				otherCustomerID := uuid.NewString()
				otherClusterID := uuid.NewString()
				require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: otherCustomerID, Name: test.name, Slug: otherCustomerID}))
				require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: otherClusterID, Name: test.name, CustomerID: otherCustomerID}))
				_, err := svc.CreateEnrollmentToken(actorCtx, connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
					CustomerId: otherCustomerID, ClusterId: otherClusterID, OperatorName: "operator-ttl", TtlMinutes: test.ttl,
				}))
				if test.wantCode == 0 {
					require.NoError(t, err)
					return
				}
				require.Error(t, err)
				assert.Equal(t, test.wantCode, connect.CodeOf(err))
			})
		}

		for _, name := range []string{"", "-operator", "operator-", "Operator", "operator_name", strings.Repeat("a", 64)} {
			_, err := svc.CreateEnrollmentToken(actorCtx, connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
				CustomerId: "customer-operator", ClusterId: "cluster-operator", OperatorName: name, TtlMinutes: 5,
			}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		}

		active := &store.Operator{ID: "operator-active-name", Name: "shared-name", CustomerID: "customer-operator", ClusterID: "cluster-operator", CertSerial: "serial-active"}
		require.NoError(t, st.Operators().Create(ctx, active))
		_, err := svc.CreateEnrollmentToken(actorCtx, connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
			CustomerId: active.CustomerID, ClusterId: active.ClusterID, OperatorName: active.Name, TtlMinutes: 5,
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))

		reason := store.SessionReasonHeartbeatTimeout
		offline := &store.Operator{ID: "operator-offline", Name: "operator-offline", CustomerID: "customer-operator", ClusterID: "cluster-operator", CertSerial: "serial-offline", Status: store.OperatorSuperseded, RegisteredAt: time.Now().UTC().Add(-time.Hour)}
		require.NoError(t, st.Operators().Create(ctx, offline))
		lastHeartbeat := time.Now().UTC().Add(-5 * time.Minute)
		require.NoError(t, st.Sessions().Create(ctx, &store.Session{ID: "session-offline", OperatorID: offline.ID, CustomerID: offline.CustomerID, ClusterID: offline.ClusterID, Status: store.SessionOffline, StatusReason: &reason, LastHeartbeat: lastHeartbeat}))
		detail, err := svc.GetOperator(actorCtx, connect.NewRequest(&orchestratorv1.GetOperatorRequest{CustomerId: offline.CustomerID, ClusterId: offline.ClusterID, OperatorId: offline.ID}))
		require.NoError(t, err)
		assert.Equal(t, orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_OFFLINE, detail.Msg.GetOperator().GetSummary().GetSessionStatus())
		assert.Equal(t, orchestratorv1.OperatorSessionStatusReason_OPERATOR_SESSION_STATUS_REASON_HEARTBEAT_TIMEOUT, detail.Msg.GetOperator().GetSummary().GetSessionStatusReason())
		assert.Equal(t, lastHeartbeat.Unix(), detail.Msg.GetOperator().GetSummary().GetLastHeartbeat().AsTime().Unix())

		for _, lifecycle := range []store.OperatorStatus{store.OperatorSuperseded, store.OperatorRevoked} {
			otherCustomerID := uuid.NewString()
			otherClusterID := uuid.NewString()
			reusedName := "reusable-name-" + string(lifecycle)
			require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: otherCustomerID, Name: reusedName, Slug: otherCustomerID}))
			require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: otherClusterID, Name: reusedName, CustomerID: otherCustomerID}))
			require.NoError(t, st.Operators().Create(ctx, &store.Operator{
				ID: uuid.NewString(), Name: reusedName, CustomerID: otherCustomerID, ClusterID: otherClusterID,
				CertSerial: uuid.NewString(), Status: lifecycle,
			}))
			_, err := svc.CreateEnrollmentToken(actorCtx, connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
				CustomerId: otherCustomerID, ClusterId: otherClusterID, OperatorName: reusedName, TtlMinutes: 5,
			}))
			require.NoError(t, err)
		}
		page, err := svc.ListOperators(actorCtx, connect.NewRequest(&orchestratorv1.ListOperatorsRequest{
			CustomerId: "customer-operator", ClusterId: "cluster-operator", PageSize: 1,
		}))
		require.NoError(t, err)
		assert.NotEmpty(t, page.Msg.GetNextPageToken())
		_, err = svc.ListOperators(actorCtx, connect.NewRequest(&orchestratorv1.ListOperatorsRequest{
			CustomerId: "customer-operator", ClusterId: "cluster-operator", PageSize: 1, PageToken: page.Msg.GetNextPageToken(),
			LifecycleStatus: func() *orchestratorv1.OperatorLifecycleStatus {
				value := orchestratorv1.OperatorLifecycleStatus_OPERATOR_LIFECYCLE_STATUS_REVOKED
				return &value
			}(),
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

		withoutSession := &store.Operator{ID: "operator-no-session", Name: "operator-no-session", CustomerID: "customer-operator", ClusterID: "cluster-operator", CertSerial: "serial-no-session", Status: store.OperatorSuperseded, RegisteredAt: time.Now().UTC().Add(-2 * time.Hour)}
		require.NoError(t, st.Operators().Create(ctx, withoutSession))
		noSession := orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_UNSPECIFIED
		filtered, err := svc.ListOperators(actorCtx, connect.NewRequest(&orchestratorv1.ListOperatorsRequest{
			CustomerId: "customer-operator", ClusterId: "cluster-operator", SessionStatus: &noSession,
		}))
		require.NoError(t, err)
		assert.GreaterOrEqual(t, filtered.Msg.GetTotalCount(), int32(1))
		assert.Contains(t, operatorSummaryIDs(filtered.Msg.GetOperators()), withoutSession.ID)

		revokeCustomerID := uuid.NewString()
		revokeClusterID := uuid.NewString()
		require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: revokeCustomerID, Name: "Revoke pending", Slug: revokeCustomerID}))
		require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: revokeClusterID, Name: "Revoke pending", CustomerID: revokeCustomerID}))
		_, err = svc.CreateEnrollmentToken(actorCtx, connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
			CustomerId: revokeCustomerID, ClusterId: revokeClusterID, OperatorName: "operator-revoke-pending", TtlMinutes: 5,
		}))
		require.NoError(t, err)

		revoked, err := svc.RevokePendingEnrollmentToken(actorCtx, connect.NewRequest(&orchestratorv1.RevokePendingEnrollmentTokenRequest{CustomerId: revokeCustomerID, ClusterId: revokeClusterID}))
		require.NoError(t, err)
		assert.True(t, revoked.Msg.GetChanged())
		again, err := svc.RevokePendingEnrollmentToken(actorCtx, connect.NewRequest(&orchestratorv1.RevokePendingEnrollmentTokenRequest{CustomerId: revokeCustomerID, ClusterId: revokeClusterID}))
		require.NoError(t, err)
		assert.False(t, again.Msg.GetChanged())
		revokedAudit, err := st.AuditEvents().Query(ctx, store.AuditEventFilter{Action: "operator.enrollment_token.revoked"}, "", 20)
		require.NoError(t, err)
		require.Len(t, revokedAudit.Events, 2)
		for _, event := range revokedAudit.Events {
			assert.Equal(t, store.AuditActorUser, event.ActorKind)
			assert.Equal(t, "user-operator", event.ActorID)
			assert.Equal(t, "org-operator", event.OrganizationID)
			assert.Equal(t, revokeCustomerID, event.Metadata["customer_id"])
			assert.Equal(t, revokeClusterID, event.Metadata["cluster_id"])
			assert.Equal(t, revokeClusterID, event.ResourceID)
			assert.Equal(t, "succeeded", event.Status)
			assert.NotEmpty(t, event.Metadata["request_id"])
			assert.NotContains(t, fmt.Sprint(event.Metadata), "plaintext")
		}
	})
}
