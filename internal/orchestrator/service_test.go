package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/audit"
	authctx "github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/authorization"
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
	verifier := trust.NewStubVerifier(st.Verifications(), nil, logger)
	svc := NewService(st, verifier, "staging", nil, authorization.NewStoreAuthorizer(st), logger)
	for _, id := range []string{"bundle-001", "bundle-002", "bundle-upgrade"} {
		seedTestBundle(t, st, id)
	}

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

	org := &store.Organization{ID: "org-001", Name: "Test Organization"}
	require.NoError(t, st.Organizations().Create(context.Background(), org))

	require.NoError(t, st.Users().Create(context.Background(), &store.User{
		ID: "user-001", Username: "user-001", Status: store.UserActive,
	}))
	require.NoError(t, st.Users().Create(context.Background(), &store.User{
		ID: "user-viewer", Username: "user-viewer", Status: store.UserActive,
	}))

	binding := &store.OrgCustomerBinding{
		ID: "binding-001", OrgID: org.ID, CustomerID: cust.ID,
	}
	require.NoError(t, st.Bindings().Create(context.Background(), binding))

	require.NoError(t, st.OrgMembers().Create(context.Background(), &store.OrganizationMember{
		OrgID: org.ID, UserID: "user-001", Role: store.RoleDeployer,
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

// REQ-012 AC-012-01: Digest mismatch rejects the operation using the stored bundle digest.
func TestCreateOperation_VerificationRejected_DigestMismatch(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	_, err := svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001", IdempotencyKey: "idem-verify-001",
		SignatureRef: &commonv1.SignatureRef{
			Digest: "sha256:wrong", Signature: "test-signature", Issuer: "release-manager-ci", Subject: "release-manager/v1.0.0",
		},
		Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))

	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "digest_mismatch")
}

// REQ-012 AC-012-05: production never skips verification when signature_ref is absent.
func TestCreateOperation_NoSignatureRefFailsClosedInProduction(t *testing.T) {
	_, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc := NewService(st, trust.NewStubVerifier(st.Verifications(), nil, logger), "production", nil, authorization.NewStoreAuthorizer(st), logger)

	_, err := svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001", IdempotencyKey: "idem-verify-002",
		Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))

	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "signature_missing")
}

func TestCreateOperation_NoSignatureRefWarnsInStaging(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	resp, err := svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001", IdempotencyKey: "idem-verify-003",
		Actor: &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))

	require.NoError(t, err)
	assert.Equal(t, commonv1.VerificationResult_VERIFICATION_RESULT_POLICY_WARNING, resp.Msg.VerificationResult)
}

func TestCreateOperation_VerificationUnavailableFailsClosedInProduction(t *testing.T) {
	_, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	svc := NewService(st, verifierFunc(func(context.Context, trust.Input) (*trust.Output, error) {
		return &trust.Output{Status: store.VerificationVerificationUnavailable, Summary: "verification_unavailable: backend offline"}, nil
	}), "production", nil, authorization.NewStoreAuthorizer(st), slog.New(slog.DiscardHandler))

	digest := "sha256:" + fmt.Sprintf("%064x", 74)
	_, err := svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001", IdempotencyKey: "idem-verify-004",
		SignatureRef: &commonv1.SignatureRef{Digest: digest, Signature: "signature", Issuer: "release-manager-ci"},
		Actor:        &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))

	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "verification_unavailable")
}

func TestCreateOperation_VerificationUnavailableWarnsInStaging(t *testing.T) {
	_, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	svc := NewService(st, verifierFunc(func(context.Context, trust.Input) (*trust.Output, error) {
		return &trust.Output{Status: store.VerificationVerificationUnavailable, Summary: "verification_unavailable: backend offline"}, nil
	}), "staging", nil, authorization.NewStoreAuthorizer(st), slog.New(slog.DiscardHandler))

	digest := "sha256:" + fmt.Sprintf("%064x", 74)
	resp, err := svc.CreateOperation(t.Context(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001", IdempotencyKey: "idem-verify-005",
		SignatureRef: &commonv1.SignatureRef{Digest: digest, Signature: "signature", Issuer: "release-manager-ci"},
		Actor:        &commonv1.ActorContext{UserId: "user-001", Organization: "org-001"},
	}))

	require.NoError(t, err)
	assert.Equal(t, commonv1.VerificationResult_VERIFICATION_RESULT_POLICY_WARNING, resp.Msg.GetVerificationResult())
}

type verifierFunc func(context.Context, trust.Input) (*trust.Output, error)

func (fn verifierFunc) Verify(ctx context.Context, input trust.Input) (*trust.Output, error) {
	return fn(ctx, input)
}

func TestCreateOperation_NonTrustedResultsEmitAudit(t *testing.T) {
	tests := []struct {
		name       string
		targetEnv  string
		verifier   verifierFunc
		signature  *commonv1.SignatureRef
		wantStatus store.VerificationStatus
		wantCode   connect.Code
	}{
		{
			name:      "rejected signature",
			targetEnv: "staging",
			verifier: func(context.Context, trust.Input) (*trust.Output, error) {
				return &trust.Output{Status: store.VerificationRejected, Summary: "signature_invalid: rejected"}, nil
			},
			signature:  &commonv1.SignatureRef{Digest: "sha256:" + fmt.Sprintf("%064x", 74), Signature: "invalid", Issuer: "release-manager-ci"},
			wantStatus: store.VerificationRejected,
			wantCode:   connect.CodeFailedPrecondition,
		},
		{
			name:      "missing signature",
			targetEnv: "production",
			verifier: func(context.Context, trust.Input) (*trust.Output, error) {
				return &trust.Output{Status: store.VerificationSignatureMissing, Summary: "signature_missing: absent"}, nil
			},
			wantStatus: store.VerificationSignatureMissing,
			wantCode:   connect.CodeFailedPrecondition,
		},
		{
			name:      "verification unavailable",
			targetEnv: "production",
			verifier: func(context.Context, trust.Input) (*trust.Output, error) {
				return &trust.Output{Status: store.VerificationVerificationUnavailable, Summary: "verification_unavailable: backend offline"}, nil
			},
			signature:  &commonv1.SignatureRef{Digest: "sha256:" + fmt.Sprintf("%064x", 74), Signature: "unavailable", Issuer: "release-manager-ci"},
			wantStatus: store.VerificationVerificationUnavailable,
			wantCode:   connect.CodeUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, st, cleanup := setupService(t)
			defer cleanup()
			seedDefinition(t, st)
			emitter := audit.NewEmitter(st.AuditEvents(), slog.New(slog.DiscardHandler), audit.EmitterConfig{BufferSize: 8, BatchSize: 1, FlushInterval: time.Hour})
			t.Cleanup(func() { require.NoError(t, emitter.Shutdown(context.Background())) })
			svc := NewService(st, tt.verifier, tt.targetEnv, emitter, authorization.NewStoreAuthorizer(st), slog.New(slog.DiscardHandler))
			ctx := authctx.WithActor(t.Context(), authctx.Actor{UserID: "trusted-user", OrganizationID: "org-001", Roles: []string{string(store.RoleDeployer)}})

			_, err := svc.CreateOperation(ctx, connect.NewRequest(&orchestratorv1.CreateOperationRequest{
				OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
				ValuesRevisionId: "vr-001", IdempotencyKey: "audit-" + strings.ReplaceAll(tt.name, " ", "-"),
				SignatureRef: tt.signature,
				Actor:        &commonv1.ActorContext{UserId: "spoofed-user", Organization: "org-001"},
			}))
			require.Error(t, err)
			assert.Equal(t, tt.wantCode, connect.CodeOf(err))
			require.NoError(t, emitter.Shutdown(context.Background()))

			events, listErr := st.AuditEvents().ListByResource(t.Context(), "release_bundle", "bundle-001")
			require.NoError(t, listErr)
			require.Len(t, events, 1)
			assert.Equal(t, "trusted-user", events[0].ActorID)
			assert.Equal(t, "org-001", events[0].OrganizationID)
			assert.Equal(t, string(tt.wantStatus), events[0].Status)
			assert.Equal(t, "sha256:"+fmt.Sprintf("%064x", 74), events[0].Metadata["digest"])
			assert.Equal(t, string(tt.wantStatus), events[0].Metadata["result"])
		})
	}
}

func seedTestBundle(t *testing.T, st store.Store, id string) string {
	t.Helper()
	digest := fmt.Sprintf("%064x", 74)
	require.NoError(t, st.Bundles().Create(t.Context(), &store.ReleaseBundle{
		ID: id, Name: "test bundle", DigestAlg: "sha256", DigestValue: digest, Status: store.BundleValidated,
		CreatedAt: time.Now().UTC(),
	}))
	return "sha256:" + digest
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

// --- GetOperation tests ---

func TestGetOperation_Success(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	require.NoError(t, st.Operations().Create(context.Background(), &store.Operation{
		ID:                  "op-get-001",
		OperationType:       store.OperationInstall,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: "def-001",
		IdempotencyKey:      "get-001-key",
		RequestHash:         "get-001-hash",
		StateVersion:        3,
		BundleID:            "bundle-get",
		ValuesRevisionID:    "vr-001",
		ExpectedRevision:    5,
		ValuesPatch:         []byte(`{"secret":"x"}`),
		Actor:               store.ActorContext{UserID: "user-001", Organization: "org-001"},
		CreatedAt:           time.Now().UTC(),
		UpdatedAt:           time.Now().UTC(),
	}))

	ctx := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-001", OrganizationID: "org-001", Roles: []string{string(store.RoleDeployer)},
	})
	resp, err := svc.GetOperation(ctx, connect.NewRequest(&orchestratorv1.GetOperationRequest{
		OperationId: "op-get-001",
	}))
	require.NoError(t, err)
	op := resp.Msg.Operation
	assert.Equal(t, "op-get-001", op.OperationId)
	assert.Equal(t, "INSTALL", op.OperationType)
	assert.Equal(t, int64(3), op.StateVersion)
	assert.Equal(t, "user-001", op.Actor.UserId)
	assert.NotContains(t, op.String(), "get-001-key")
	assert.NotContains(t, op.String(), "get-001-hash")
	assert.NotContains(t, op.String(), "secret")
}

func TestGetOperation_NotFound(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	ctx := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-001", OrganizationID: "org-001", Roles: []string{string(store.RoleViewer)},
	})
	_, err := svc.GetOperation(ctx, connect.NewRequest(&orchestratorv1.GetOperationRequest{
		OperationId: "nonexistent",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestGetOperation_CrossOrganizationDenied(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	require.NoError(t, st.Operations().Create(context.Background(), &store.Operation{
		ID: "op-cross-org", OperationType: store.OperationInstall,
		Status: store.StatusRunning, ReleaseDefinitionID: "def-001",
		IdempotencyKey: "op-cross-org-key", RequestHash: "op-cross-org-hash", StateVersion: 1,
	}))
	require.NoError(t, st.Organizations().Create(context.Background(), &store.Organization{
		ID: "org-other", Name: "Other Organization",
	}))
	require.NoError(t, st.Users().Create(context.Background(), &store.User{
		ID: "user-other", Username: "user-other", Status: store.UserActive,
	}))
	require.NoError(t, st.OrgMembers().Create(context.Background(), &store.OrganizationMember{
		OrgID: "org-other", UserID: "user-other", Role: store.RoleViewer,
	}))

	ctx := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-other", OrganizationID: "org-other", Roles: []string{string(store.RoleViewer)},
	})
	_, err := svc.GetOperation(ctx, connect.NewRequest(&orchestratorv1.GetOperationRequest{
		OperationId: "op-cross-org",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestGetOperation_TerminalAtPresentAfterTransition(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	require.NoError(t, st.Operations().Create(context.Background(), &store.Operation{
		ID: "op-term-001", OperationType: store.OperationInstall,
		Status: store.StatusRunning, ReleaseDefinitionID: "def-001",
		IdempotencyKey: "term-001-key", RequestHash: "term-001-hash", StateVersion: 1,
	}))

	_, err := st.Operations().Transition(context.Background(), "op-term-001", store.StatusSucceeded, 1, "")
	require.NoError(t, err)

	ctx := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-001", OrganizationID: "org-001", Roles: []string{string(store.RoleViewer)},
	})
	resp, err := svc.GetOperation(ctx, connect.NewRequest(&orchestratorv1.GetOperationRequest{
		OperationId: "op-term-001",
	}))
	require.NoError(t, err)
	assert.NotNil(t, resp.Msg.Operation.TerminalAt)
}

// --- CancelOperation tests ---

func deployerCtx() context.Context {
	return authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-001", OrganizationID: "org-001", Roles: []string{string(store.RoleDeployer)},
	})
}

func seedCancelableOperation(t *testing.T, st store.Store, id string, status store.OperationStatus) {
	t.Helper()
	require.NoError(t, st.Operations().Create(context.Background(), &store.Operation{
		ID: id, OperationType: store.OperationInstall, Status: status,
		ReleaseDefinitionID: "def-001",
		IdempotencyKey:      id + "-key", RequestHash: id + "-hash", StateVersion: 1,
	}))
}

func TestCancelOperation_SuccessPending(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedCancelableOperation(t, st, "op-pending", store.StatusPending)

	req := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: "op-pending", Reason: "no longer needed", ExpectedStateVersion: 1,
	})
	req.Header().Set("Idempotency-Key", "cancel-pending")
	resp, err := svc.CancelOperation(deployerCtx(), req)
	require.NoError(t, err)
	assert.Equal(t, orchestratorv1.OperationStatus_OPERATION_STATUS_CANCELLED, resp.Msg.Operation.State)
	assert.NotEmpty(t, resp.Msg.RequestId)
}

func TestCancelOperation_SuccessRunningToCancelling(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedCancelableOperation(t, st, "op-running", store.StatusRunning)

	req := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: "op-running", Reason: "must rollback", ExpectedStateVersion: 1,
	})
	req.Header().Set("Idempotency-Key", "cancel-running")
	resp, err := svc.CancelOperation(deployerCtx(), req)
	require.NoError(t, err)
	assert.Equal(t, orchestratorv1.OperationStatus_OPERATION_STATUS_CANCELLING, resp.Msg.Operation.State)
	assert.Equal(t, int64(2), resp.Msg.Operation.StateVersion)
}

func TestCancelOperation_ConcurrentOnlyOneTransition(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedCancelableOperation(t, st, "op-concurrent-cancel", store.StatusRunning)

	const requests = 8
	errorsCh := make(chan error, requests)
	for i := range requests {
		go func(i int) {
			req := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
				OperationId: "op-concurrent-cancel", Reason: "concurrent cancel", ExpectedStateVersion: 1,
			})
			req.Header().Set("Idempotency-Key", fmt.Sprintf("cancel-concurrent-%d", i))
			_, err := svc.CancelOperation(deployerCtx(), req)
			errorsCh <- err
		}(i)
	}

	accepted := 0
	conflicts := 0
	for range requests {
		err := <-errorsCh
		switch {
		case err == nil:
			accepted++
		case connect.CodeOf(err) == connect.CodeAborted || connect.CodeOf(err) == connect.CodeFailedPrecondition:
			conflicts++
		default:
			t.Fatalf("unexpected concurrent cancel error: %v", err)
		}
	}
	assert.Equal(t, 1, accepted)
	assert.Equal(t, requests-1, conflicts)

	persisted, err := st.Operations().Get(context.Background(), "op-concurrent-cancel")
	require.NoError(t, err)
	assert.Equal(t, store.StatusCancelling, persisted.Status)
	assert.Equal(t, 2, persisted.StateVersion)
}

func TestCancelOperation_CancellingRejectsNewIntent(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedCancelableOperation(t, st, "op-cancelling", store.StatusRunning)

	firstReq := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: "op-cancelling", Reason: "stop rollout", ExpectedStateVersion: 1,
	})
	firstReq.Header().Set("Idempotency-Key", "cancel-cancelling-first")
	first, err := svc.CancelOperation(deployerCtx(), firstReq)
	require.NoError(t, err)
	assert.Equal(t, orchestratorv1.OperationStatus_OPERATION_STATUS_CANCELLING, first.Msg.Operation.State)

	secondReq := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: "op-cancelling", Reason: "second cancellation intent", ExpectedStateVersion: 2,
	})
	secondReq.Header().Set("Idempotency-Key", "cancel-cancelling-second")
	_, err = svc.CancelOperation(deployerCtx(), secondReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "cancel_not_allowed")

	persisted, err := st.Operations().Get(context.Background(), "op-cancelling")
	require.NoError(t, err)
	assert.Equal(t, store.StatusCancelling, persisted.Status)
	assert.Equal(t, 2, persisted.StateVersion)
}

func TestCancelOperation_ReasonUsesUnicodeCharacters(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedCancelableOperation(t, st, "op-unicode-reason", store.StatusPending)

	req := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: "op-unicode-reason", Reason: strings.Repeat("界", 500), ExpectedStateVersion: 1,
	})
	req.Header().Set("Idempotency-Key", "cancel-unicode-reason")
	resp, err := svc.CancelOperation(deployerCtx(), req)
	require.NoError(t, err)
	assert.Equal(t, orchestratorv1.OperationStatus_OPERATION_STATUS_CANCELLED, resp.Msg.Operation.State)
}

func TestCancelOperation_RunningIdempotencyReplay(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedCancelableOperation(t, st, "op-running-idem", store.StatusRunning)

	request := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: "op-running-idem", Reason: "stop rollout", ExpectedStateVersion: 1,
	})
	request.Header().Set("Idempotency-Key", "cancel-running-idem")
	first, err := svc.CancelOperation(deployerCtx(), request)
	require.NoError(t, err)

	replayed, err := svc.CancelOperation(deployerCtx(), request)
	require.NoError(t, err)
	assert.Equal(t, first.Msg.RequestId, replayed.Msg.RequestId)
	assert.Equal(t, orchestratorv1.OperationStatus_OPERATION_STATUS_CANCELLING, replayed.Msg.Operation.State)
	assert.Equal(t, int64(2), replayed.Msg.Operation.StateVersion)
}

func TestCancelOperation_TerminalRejected(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedCancelableOperation(t, st, "op-term", store.StatusRunning)
	updated, err := st.Operations().Transition(context.Background(), "op-term", store.StatusSucceeded, 1, "")
	require.NoError(t, err)

	req := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: "op-term", Reason: "trying to cancel", ExpectedStateVersion: int64(updated.StateVersion),
	})
	req.Header().Set("Idempotency-Key", "cancel-term")
	_, err = svc.CancelOperation(deployerCtx(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "cancel_not_allowed")
}

func TestCancelOperation_CASConflict(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedCancelableOperation(t, st, "op-cas", store.StatusRunning)

	req := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: "op-cas", Reason: "cancel cas test", ExpectedStateVersion: 99,
	})
	req.Header().Set("Idempotency-Key", "cancel-cas")
	_, err := svc.CancelOperation(deployerCtx(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "optimistic_lock_conflict")
}

func TestCancelOperation_Idempotency(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedCancelableOperation(t, st, "op-idem", store.StatusPending)

	ctx := deployerCtx()
	firstReq := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: "op-idem", Reason: "cancel idempotency", ExpectedStateVersion: 1,
	})
	firstReq.Header().Set("Idempotency-Key", "cancel-idem-key")
	resp1, err := svc.CancelOperation(ctx, firstReq)
	require.NoError(t, err)

	secondReq := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: "op-idem", Reason: "cancel idempotency", ExpectedStateVersion: 1,
	})
	secondReq.Header().Set("Idempotency-Key", "cancel-idem-key")
	resp2, err := svc.CancelOperation(ctx, secondReq)
	require.NoError(t, err)
	assert.Equal(t, resp1.Msg.Operation.State, resp2.Msg.Operation.State)
	assert.Equal(t, resp1.Msg.RequestId, resp2.Msg.RequestId)
}

func TestCancelOperation_IdempotencyConflict(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedCancelableOperation(t, st, "op-conflict", store.StatusPending)

	ctx := deployerCtx()
	firstReq := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: "op-conflict", Reason: "first reason", ExpectedStateVersion: 1,
	})
	firstReq.Header().Set("Idempotency-Key", "cancel-conflict-key")
	_, err := svc.CancelOperation(ctx, firstReq)
	require.NoError(t, err)

	secondReq := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: "op-conflict", Reason: "different reason", ExpectedStateVersion: 1,
	})
	secondReq.Header().Set("Idempotency-Key", "cancel-conflict-key")
	_, err = svc.CancelOperation(ctx, secondReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "idempotency_conflict")
}

func TestCancelOperation_Validation(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()
	ctx := deployerCtx()

	tests := []struct {
		name           string
		req            *orchestratorv1.CancelOperationRequest
		idempotencyKey string
		wantErr        bool
		wantMsg        string
	}{
		{
			name: "missing idempotency_key", req: &orchestratorv1.CancelOperationRequest{
				OperationId: "op", Reason: "test", ExpectedStateVersion: 1,
			},
			wantErr: true, wantMsg: "Idempotency-Key",
		},
		{
			name: "missing operation_id", req: &orchestratorv1.CancelOperationRequest{
				Reason: "test", ExpectedStateVersion: 1,
			},
			idempotencyKey: "k1", wantErr: true, wantMsg: "operation_id",
		},
		{
			name: "missing reason", req: &orchestratorv1.CancelOperationRequest{
				OperationId: "op", ExpectedStateVersion: 1,
			},
			idempotencyKey: "k2", wantErr: true, wantMsg: "reason",
		},
		{
			name: "blank reason", req: &orchestratorv1.CancelOperationRequest{
				OperationId: "op", Reason: "   ", ExpectedStateVersion: 1,
			},
			idempotencyKey: "k3", wantErr: true, wantMsg: "reason",
		},
		{
			name: "invalid expected_state_version", req: &orchestratorv1.CancelOperationRequest{
				OperationId: "op", Reason: "test", ExpectedStateVersion: 0,
			},
			idempotencyKey: "k4", wantErr: true, wantMsg: "expected_state_version",
		},
		{
			name: "reason too long", req: &orchestratorv1.CancelOperationRequest{
				OperationId: "op", Reason: strings.Repeat("x", 501), ExpectedStateVersion: 1,
			},
			idempotencyKey: "k5", wantErr: true, wantMsg: "exceeds 500",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := connect.NewRequest(tt.req)
			if tt.idempotencyKey != "" {
				req.Header().Set("Idempotency-Key", tt.idempotencyKey)
			}
			_, err := svc.CancelOperation(ctx, req)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCancelOperation_ViewerDenied(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedCancelableOperation(t, st, "op-viewer", store.StatusPending)
	require.NoError(t, st.OrgMembers().Create(context.Background(), &store.OrganizationMember{
		OrgID: "org-001", UserID: "user-viewer", Role: store.RoleViewer,
	}))

	ctx := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-viewer", OrganizationID: "org-001", Roles: []string{string(store.RoleViewer)},
	})
	req := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: "op-viewer", Reason: "try cancel", ExpectedStateVersion: 1,
	})
	req.Header().Set("Idempotency-Key", "cancel-viewer")
	_, err := svc.CancelOperation(ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "role_insufficient")
}

func TestCancelOperation_PreflightLifecycleNullOnMissingLifecycle(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedCancelableOperation(t, st, "op-no-pl", store.StatusPending)

	req := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: "op-no-pl", Reason: "cancel before preflight", ExpectedStateVersion: 1,
	})
	req.Header().Set("Idempotency-Key", "cancel-no-pl")
	resp, err := svc.CancelOperation(deployerCtx(), req)
	require.NoError(t, err)
	assert.Equal(t, orchestratorv1.OperationStatus_OPERATION_STATUS_CANCELLED, resp.Msg.Operation.State)
	assert.NotNil(t, resp.Msg.Operation.TerminalAt)

	op, err := st.Operations().Get(context.Background(), "op-no-pl")
	require.NoError(t, err)
	assert.Equal(t, store.StatusCancelled, op.Status)
	assert.NotNil(t, op.TerminalAt)
}

func seedTimelineEntry(t *testing.T, st store.Store, opID string, stateVersion int) *store.OperationTimelineEntry {
	t.Helper()
	entry, err := st.Timeline().Append(context.Background(), &store.OperationTimelineEntry{
		OperationID:           opID,
		Kind:                  string(store.TimelineEntryStateTransition),
		OperationStateVersion: stateVersion,
		Data:                  json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	require.NotNil(t, entry)
	return entry
}

func TestWatchOperation_Validation(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()
	err := svc.WatchOperation(deployerCtx(), &connect.Request[orchestratorv1.WatchOperationRequest]{
		Msg: &orchestratorv1.WatchOperationRequest{OperationId: ""},
	}, nil)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestWatchOperation_NotFound(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()
	err := svc.WatchOperation(deployerCtx(), &connect.Request[orchestratorv1.WatchOperationRequest]{
		Msg: &orchestratorv1.WatchOperationRequest{OperationId: "nonexistent", AfterSequence: 0},
	}, nil)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestWatchOperation_SnapshotAndReplay(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	// Create a terminal operation with timeline entries.
	op := &store.Operation{
		ID: "op-watch-1", OperationType: store.OperationInstall,
		ReleaseDefinitionID: "def-001",
		Status:              store.StatusSucceeded,
		IdempotencyKey:      "watch-ik", RequestHash: "watch-rh",
		StateVersion: 3, TerminalAt: timeNow(),
	}
	require.NoError(t, st.Operations().Create(context.Background(), op))

	// Seed timeline entries for replay.
	entry1 := seedTimelineEntry(t, st, "op-watch-1", 1)
	entry2 := seedTimelineEntry(t, st, "op-watch-1", 2)
	require.Equal(t, int64(1), entry1.Sequence)
	require.Equal(t, int64(2), entry2.Sequence)

	// Build a Connect server with the handler and test auth interceptor.
	authInt := &testAuthInterceptor{
		Actor: authctx.Actor{UserID: "user-001", OrganizationID: "org-001", Roles: []string{string(store.RoleDeployer)}},
	}
	mux := http.NewServeMux()
	path, handler := orchestratorv1connect.NewOrchestratorServiceHandler(svc, connect.WithInterceptors(authInt))
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := orchestratorv1connect.NewOrchestratorServiceClient(http.DefaultClient, srv.URL)

	ctx, cancel := context.WithCancel(deployerCtx())
	defer cancel()

	stream, err := client.WatchOperation(ctx, connect.NewRequest(&orchestratorv1.WatchOperationRequest{
		OperationId: "op-watch-1", AfterSequence: 0,
	}))
	require.NoError(t, err)
	require.NotNil(t, stream)

	// Receive messages in a goroutine, then cancel after collecting snapshot + replay.
	var snapshot *orchestratorv1.OperationSnapshot
	var entries []*orchestratorv1.TimelineEntry
	done := make(chan struct{})
	go func() {
		defer close(done)
		for stream.Receive() {
			msg := stream.Msg()
			switch p := msg.Payload.(type) {
			case *orchestratorv1.WatchOperationResponse_Snapshot:
				snapshot = p.Snapshot
			case *orchestratorv1.WatchOperationResponse_Entry:
				entries = append(entries, p.Entry)
			}
		}
	}()
	// Give the handler time to send snapshot + replay entries (50ms poll, so 200ms is enough).
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	if stream.Err() != nil && !errors.Is(stream.Err(), io.EOF) && !errors.Is(stream.Err(), context.Canceled) {
		require.NoError(t, stream.Err())
	}

	require.NotNil(t, snapshot)
	assert.Equal(t, "op-watch-1", snapshot.Operation.GetOperationId())
	assert.Equal(t, int64(2), snapshot.SnapshotSequence)     // max sequence
	assert.Equal(t, int64(1), snapshot.RetainedFromSequence) // min sequence
	assert.Equal(t, orchestratorv1.OperationStatus_OPERATION_STATUS_SUCCEEDED, snapshot.Operation.State)

	require.Len(t, entries, 2)
	assert.Equal(t, int64(1), entries[0].Sequence)
	assert.Equal(t, int64(2), entries[1].Sequence)
}

func TestWatchOperation_AfterSequenceSkipsEntries(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	op := &store.Operation{
		ID: "op-watch-2", OperationType: store.OperationInstall,
		ReleaseDefinitionID: "def-001",
		Status:              store.StatusSucceeded,
		IdempotencyKey:      "watch2-ik", RequestHash: "watch2-rh",
		StateVersion: 2, TerminalAt: timeNow(),
	}
	require.NoError(t, st.Operations().Create(context.Background(), op))

	seedTimelineEntry(t, st, "op-watch-2", 1) // seq 1, should be skipped
	seedTimelineEntry(t, st, "op-watch-2", 2) // seq 2, should be included

	authInt := &testAuthInterceptor{
		Actor: authctx.Actor{UserID: "user-001", OrganizationID: "org-001", Roles: []string{string(store.RoleDeployer)}},
	}
	mux := http.NewServeMux()
	path, handler := orchestratorv1connect.NewOrchestratorServiceHandler(svc, connect.WithInterceptors(authInt))
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := orchestratorv1connect.NewOrchestratorServiceClient(http.DefaultClient, srv.URL)

	ctx, cancel := context.WithCancel(deployerCtx())
	defer cancel()

	stream, err := client.WatchOperation(ctx, connect.NewRequest(&orchestratorv1.WatchOperationRequest{
		OperationId: "op-watch-2", AfterSequence: 1, // skip seq 1
	}))
	require.NoError(t, err)
	require.NotNil(t, stream)

	var replayEntries []*orchestratorv1.TimelineEntry
	var gotSnapshot bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for stream.Receive() {
			msg := stream.Msg()
			switch p := msg.Payload.(type) {
			case *orchestratorv1.WatchOperationResponse_Snapshot:
				gotSnapshot = true
			case *orchestratorv1.WatchOperationResponse_Entry:
				replayEntries = append(replayEntries, p.Entry)
			}
		}
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	if stream.Err() != nil && !errors.Is(stream.Err(), io.EOF) && !errors.Is(stream.Err(), context.Canceled) {
		require.NoError(t, stream.Err())
	}

	assert.True(t, gotSnapshot)
	require.Len(t, replayEntries, 1)
	assert.Equal(t, int64(2), replayEntries[0].Sequence)
}

func timeNow() *time.Time {
	t := time.Now().UTC().Truncate(time.Millisecond)
	return &t
}

// testAuthInterceptor injects a deployer actor into the context for test HTTP calls.
type testAuthInterceptor struct {
	authctx.Actor
}

func (i *testAuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx = authctx.WithActor(ctx, i.Actor)
		return next(ctx, req)
	}
}

func (i *testAuthInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *testAuthInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx = authctx.WithActor(ctx, i.Actor)
		return next(ctx, conn)
	}
}
