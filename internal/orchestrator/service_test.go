package orchestrator

import (
	"context"
	"encoding/base64"
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
	"github.com/google/uuid"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/auth"
	authctx "github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/orchestrator/preflight"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/internal/trust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
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

// seedActorBinding provisions the org-001/user-001 release_admin actor and an
// active binding to the given customer, satisfying the REQ-067 authorization
// precondition for tests that build their own definitions.
func seedActorBinding(t *testing.T, st store.Store, customerID string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: "org-001", Name: "Test Organization"}))
	require.NoError(t, st.Users().Create(ctx, &store.User{ID: "user-001", Username: "user-001", Status: store.UserActive}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: "org-001", UserID: "user-001", Role: store.RoleReleaseAdmin,
	}))
	require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{
		ID: "binding-" + customerID, OrgID: "org-001", CustomerID: customerID,
	}))
}

// sqliteUOW extracts the operation creation UOW from a store returned by
// setupService (concrete *sqlite.Store behind the store.Store interface).
func sqliteUOW(st store.Store) store.OperationCreationUnitOfWork {
	if provider, ok := st.(interface {
		OperationCreationUnitOfWork() store.OperationCreationUnitOfWork
	}); ok {
		return provider.OperationCreationUnitOfWork()
	}
	return nil
}

// seedUpgradeInventory creates an active inventory entry for def-001 at
// revision 1, satisfying the UPGRADE inventory precondition (REQ-067 rule 13).
func seedUpgradeInventory(t *testing.T, st store.Store) {
	t.Helper()
	require.NoError(t, st.Inventories().Upsert(t.Context(), &store.ReleaseInventory{
		ReleaseDefinitionID: "def-001",
		CustomerID:          "cust-001",
		ClusterID:           "cls-001",
		Namespace:           "default",
		ReleaseName:         "my-release",
		InventoryStatus:     store.InventoryActive,
		Revision:            1,
	}))
}

// adminCtx returns a context with a release_admin actor for org-001
// (REQ-067 rule 2: actor comes from the auth interceptor context).
func adminCtx() context.Context {
	return authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-001", OrganizationID: "org-001", Roles: []string{string(store.RoleReleaseAdmin)},
	})
}

// withIdempotencyKey sets the HTTP Idempotency-Key header on a request
// (REQ-067 rule 5: the key travels via the header, not the body).
func withIdempotencyKey[Req any](req *connect.Request[Req], key string) *connect.Request[Req] {
	req.Header().Set("Idempotency-Key", key)
	return req
}

func setupService(t *testing.T) (*Service, store.Store, func()) {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	st, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	verifier := trust.NewStubVerifier(st.Verifications(), nil, logger)
	svc := NewService(st, verifier, "staging", nil, st.OperationCreationUnitOfWork(), authorization.NewStoreAuthorizer(st), logger)
	for _, id := range []string{"bundle-001", "bundle-002", "bundle-upgrade"} {
		seedTestBundle(t, st, id)
	}

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
	err := st.Definitions().Create(context.Background(), def, nil)
	require.NoError(t, err)

	revision := &store.ValuesRevision{
		ID:                  "vr-001",
		ReleaseDefinitionID: def.ID,
		Version:             1,
		Status:              store.ValuesStatusApproved,
		CanonicalDocument:   []byte(`{"message":"hello"}`),
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
	nextVersion, err := st.Values().GetNextRevisionNumber(context.Background(), definitionID)
	require.NoError(t, err)
	parentRevisionID := ""
	if nextVersion > 1 {
		latest, latestErr := st.Values().GetLatest(context.Background(), definitionID)
		require.NoError(t, latestErr)
		parentRevisionID = latest.ID
	}
	revision := &store.ValuesRevision{
		ID:                  id,
		ReleaseDefinitionID: definitionID,
		Version:             nextVersion,
		StateVersion:        1,
		Status:              initialStatus,
		CanonicalDocument:   []byte(`{"replicas":2}`),
		Digest:              "sha256:test",
		ParentRevisionID:    parentRevisionID,
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

func upgradeRequest(valuesRevisionID string) *connect.Request[orchestratorv1.CreateOperationRequest] {
	req := connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:           "UPGRADE",
		BundleId:                "bundle-upgrade",
		ReleaseDefinitionId:     "def-001",
		ValuesRevisionId:        valuesRevisionID,
		ExpectedCurrentRevision: 1,
	})
	req.Header().Set("Idempotency-Key", "idem-upgrade-"+valuesRevisionID)
	return req
}

func TestCreateOperation_Install_Success(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	resp, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:           "INSTALL",
		BundleId:                "bundle-001",
		ReleaseDefinitionId:     "def-001",
		ValuesRevisionId:        "vr-001",
		ExpectedCurrentRevision: 0,
	}), "idem-001"))
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

	_, err = svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
	}), "revoked-binding"))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
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
	}

	resp1, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(msg), "idem-dup"))
	require.NoError(t, err)

	resp2, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(msg), "idem-dup"))
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
	}
	_, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(first), "idem-conflict"))
	require.NoError(t, err)

	conflicting := &orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-002",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
	}
	_, err = svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(conflicting), "idem-conflict"))
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
	_, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:           "UPGRADE",
		BundleId:                "bundle-002",
		ReleaseDefinitionId:     "def-001",
		ValuesRevisionId:        "vr-002",
		ExpectedCurrentRevision: 1,
	}), "idem-003"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "release_busy")
}

func TestCreateOperation_ConcurrentUpgradeOnlyOneAccepted(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedUpgradeInventory(t, st)
	seedValuesRevision(t, st, "vr-concurrent", "def-001", store.ValuesStatusApproved)

	const requests = 8
	results := make(chan error, requests)
	for i := range requests {
		go func(i int) {
			req := upgradeRequest("vr-concurrent")
			req.Header().Set("Idempotency-Key", fmt.Sprintf("idem-concurrent-%d", i))
			_, err := svc.CreateOperation(adminCtx(), req)
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
			seedUpgradeInventory(t, st)
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
				tt.mutate(req.Msg)
			}

			resp, err := svc.CreateOperation(adminCtx(), req)
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
	seedUpgradeInventory(t, st)
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

	resp, err := svc.CreateOperation(adminCtx(), upgradeRequest("vr-approved"))
	require.NoError(t, err)
	require.NotNil(t, resp)

	otherOperations, err := st.Operations().List(context.Background(), other.ID)
	require.NoError(t, err)
	assert.Empty(t, otherOperations)
}

// TASK-082 AC-082-02 (D-108 ①b): an UPGRADE CreateOperation must not persist
// the unconsumed :artifact dispatch row — runUpgrade builds the :execute entry
// itself, so the first outbox entry deliverPending sends is the PayloadVersion=2
// upgrade command. A stage-shaped :artifact first row poisoned the operator
// stream with unsupported_command_version and left the operation stuck in
// RUNNING / CANCELLING (real smoke op 27f05688).
func TestCreateOperation_UpgradeDispatchesExecuteOnly(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedUpgradeInventory(t, st)

	// Active operator for cls-001 so runUpgrade can resolve a target.
	require.NoError(t, st.Operators().Create(context.Background(), &store.Operator{
		ID: "operator-upgrade", Name: "upgrade-operator", CustomerID: "cust-001",
		ClusterID: "cls-001", CertSerial: "serial-upgrade", Status: store.OperatorActive,
	}))

	// Bundle with an image override whose values_path points into an image
	// object the approved values do not carry (D-108 repro shape).
	digest := fmt.Sprintf("%064x", 75)
	require.NoError(t, st.Bundles().Create(context.Background(), &store.ReleaseBundle{
		ID: "bundle-upgrade-img", Name: "upgrade bundle", DigestAlg: "sha256", DigestValue: digest,
		Status: store.BundleValidated, ChartRef: "nginx", ChartDigest: "sha256:" + digest,
		CreatedAt: time.Now().UTC(),
		Images:    []store.BundleImage{{Ref: "registry.example.com/app", Digest: "sha256:img", ValuesPath: "image.repository"}},
	}))

	resp, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:           "UPGRADE",
		BundleId:                "bundle-upgrade-img",
		ReleaseDefinitionId:     "def-001",
		ValuesRevisionId:        "vr-001",
		ExpectedCurrentRevision: 1,
	}), "idem-upgrade-exec"))
	require.NoError(t, err)
	opID := resp.Msg.OperationId

	// runUpgrade builds the :execute entry in the background coordinator
	// goroutine; the merge tolerates the missing image object (AC-082-01).
	var execute *store.OutboxEntry
	require.Eventually(t, func() bool {
		entry, err := st.Outbox().GetByCommandID(context.Background(), opID+":execute")
		if err != nil {
			return false
		}
		execute = entry
		return true
	}, 5*time.Second, 20*time.Millisecond, "runUpgrade must dispatch the :execute command")

	payload, err := preflight.UnmarshalCommandPayload(execute.Payload)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), payload.PayloadVersion)
	require.NotNil(t, payload.Upgrade)
	assert.JSONEq(t, `{"message":"hello","image":{"repository":"registry.example.com/app@sha256:img"}}`,
		string(payload.Upgrade.GetEffectiveValuesJson()))

	// The operation reaches queued — the :execute entry is the authoritative
	// dispatch and the tolerantly merged values do not render_failed.
	require.Eventually(t, func() bool {
		op, err := st.Operations().Get(context.Background(), opID)
		return err == nil && op.Status == store.StatusQueued
	}, 5*time.Second, 20*time.Millisecond, "UPGRADE must reach queued")

	// No :artifact row may exist for UPGRADE. The dispatch is committed
	// atomically with the operation, so checking after queued is reached is
	// deterministic: a poison row would already be there.
	_, err = st.Outbox().GetByCommandID(context.Background(), opID+":artifact")
	assert.ErrorIs(t, err, store.ErrNotFound, "UPGRADE must not persist an :artifact dispatch")

	// deliverPending first delivery: GetNextPending must return the :execute
	// upgrade command (OperationType/阶段一致), never a stage-shaped payload.
	next, err := st.Outbox().GetNextPending(context.Background(), "operator-upgrade")
	require.NoError(t, err)
	assert.Equal(t, opID+":execute", next.CommandID)
	assert.Equal(t, string(store.OperationUpgrade), next.OperationType)
	decoded, err := preflight.UnmarshalCommandPayload(next.Payload)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), decoded.PayloadVersion)
	assert.NotNil(t, decoded.Upgrade)
}

// TASK-082 AC-082-03 (D-108): with the dispatch fix in place the full UPGRADE
// chain reaches terminal states through the authoritative seams — the
// operator's result drives queued→running→succeeded, and a CancelOperation on
// a running UPGRADE waits for the agent's helm_cancelled ack (cancelling→
// cancelled) instead of staying stuck in RUNNING / CANCELLING (AC-023-04).
func TestCreateOperation_UpgradeTerminalAndCancelRegression(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedUpgradeInventory(t, st)
	require.NoError(t, st.Operators().Create(context.Background(), &store.Operator{
		ID: "operator-upgrade", Name: "upgrade-operator", CustomerID: "cust-001",
		ClusterID: "cls-001", CertSerial: "serial-upgrade", Status: store.OperatorActive,
	}))
	digest := fmt.Sprintf("%064x", 76)
	require.NoError(t, st.Bundles().Create(context.Background(), &store.ReleaseBundle{
		ID: "bundle-upgrade-img", Name: "upgrade bundle", DigestAlg: "sha256", DigestValue: digest,
		Status: store.BundleValidated, ChartRef: "nginx", ChartDigest: "sha256:" + digest,
		CreatedAt: time.Now().UTC(),
		Images:    []store.BundleImage{{Ref: "registry.example.com/app", Digest: "sha256:img", ValuesPath: "image.repository"}},
	}))

	createUpgrade := func(key string) string {
		resp, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
			OperationType:           "UPGRADE",
			BundleId:                "bundle-upgrade-img",
			ReleaseDefinitionId:     "def-001",
			ValuesRevisionId:        "vr-001",
			ExpectedCurrentRevision: 1,
		}), key))
		require.NoError(t, err)
		return resp.Msg.OperationId
	}
	waitQueued := func(opID string) *store.Operation {
		var op *store.Operation
		require.Eventually(t, func() bool {
			got, err := st.Operations().Get(context.Background(), opID)
			if err != nil || got.Status != store.StatusQueued {
				return false
			}
			op = got
			return true
		}, 5*time.Second, 20*time.Millisecond, "UPGRADE %s must reach queued", opID)
		return op
	}

	// Part 1: success terminal — the operator's authoritative result drives
	// queued→running→succeeded.
	op1 := createUpgrade("idem-upgrade-terminal")
	queued := waitQueued(op1)
	running, err := st.Operations().UpdateStatus(context.Background(), op1, store.StatusRunning, queued.StateVersion, "")
	require.NoError(t, err)
	require.NoError(t, st.UpgradeResults().FinalizeUpgrade(context.Background(), &store.UpgradeTerminalInput{
		OperationID: op1, ExpectedStateVersion: running.StateVersion, Status: store.StatusSucceeded,
		ReleaseDefinitionID: "def-001", ResultPayload: []byte(`{}`),
	}))
	got1, err := st.Operations().Get(context.Background(), op1)
	require.NoError(t, err)
	assert.Equal(t, store.StatusSucceeded, got1.Status)

	// Part 2: cancel terminal regression — running→cancelling is not a fake
	// terminal (AC-023-04); the agent's helm_cancelled result finalizes it.
	op2 := createUpgrade("idem-upgrade-cancel")
	queued2 := waitQueued(op2)
	running2, err := st.Operations().UpdateStatus(context.Background(), op2, store.StatusRunning, queued2.StateVersion, "")
	require.NoError(t, err)

	cancelReq := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: op2, Reason: "cancel during upgrade", ExpectedStateVersion: int64(running2.StateVersion),
	})
	cancelReq.Header().Set("Idempotency-Key", "cancel-upgrade-running")
	cancelResp, err := svc.CancelOperation(adminCtx(), cancelReq)
	require.NoError(t, err)
	assert.Equal(t, orchestratorv1.OperationStatus_OPERATION_STATUS_CANCELLING, cancelResp.Msg.Operation.State)

	cancelling, err := st.Operations().Get(context.Background(), op2)
	require.NoError(t, err)
	assert.Equal(t, store.StatusCancelling, cancelling.Status, "CancelOperation must await the agent ack, not fake a terminal state")

	// Agent authoritative result: helm_cancelled → cancelled (the :execute
	// command was deliverable, so the operator can report back).
	require.NoError(t, st.UpgradeResults().FinalizeUpgrade(context.Background(), &store.UpgradeTerminalInput{
		OperationID: op2, ExpectedStateVersion: cancelling.StateVersion, Status: store.StatusCancelled,
		LastError: "helm_cancelled", ReleaseDefinitionID: "def-001", ResultPayload: []byte(`{}`),
	}))
	got2, err := st.Operations().Get(context.Background(), op2)
	require.NoError(t, err)
	assert.Equal(t, store.StatusCancelled, got2.Status, "helm_cancelled must drive cancelling→cancelled")

	// A second cancel on the terminal operation is rejected — nothing is left
	// stuck in cancelling.
	secondReq := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: op2, Reason: "again", ExpectedStateVersion: int64(got2.StateVersion),
	})
	secondReq.Header().Set("Idempotency-Key", "cancel-upgrade-again")
	_, err = svc.CancelOperation(adminCtx(), secondReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "cancel_not_allowed")
}

func TestCreateOperation_DefinitionNotFound(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	_, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		ReleaseDefinitionId: "nonexistent",
	}), "idem-004"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCreateOperation_InvalidType(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	_, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INVALID",
		ReleaseDefinitionId: "def-001",
	}), "idem-005"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// REQ-012 AC-012-01: Digest mismatch rejects the operation using the stored bundle digest.
func TestCreateOperation_VerificationRejected_DigestMismatch(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	_, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001",
		SignatureRef: &commonv1.SignatureRef{
			Digest: "sha256:wrong", Signature: "test-signature", Issuer: "release-manager-ci", Subject: "release-manager/v1.0.0",
		},
	}), "idem-verify-001"))

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

	_, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001",
	}), "idem-verify-002"))

	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "signature_missing")
}

func TestCreateOperation_NoSignatureRefWarnsInStaging(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	resp, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001",
	}), "idem-verify-003"))

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
	_, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001",
		SignatureRef:     &commonv1.SignatureRef{Digest: digest, Signature: "signature", Issuer: "release-manager-ci"},
	}), "idem-verify-004"))

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
	}), "staging", nil, sqliteUOW(st), authorization.NewStoreAuthorizer(st), slog.New(slog.DiscardHandler))

	digest := "sha256:" + fmt.Sprintf("%064x", 74)
	resp, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
		ValuesRevisionId: "vr-001",
		SignatureRef:     &commonv1.SignatureRef{Digest: digest, Signature: "signature", Issuer: "release-manager-ci"},
	}), "idem-verify-005"))

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
			svc := NewService(st, tt.verifier, tt.targetEnv, emitter, sqliteUOW(st), authorization.NewStoreAuthorizer(st), slog.New(slog.DiscardHandler))
			ctx := adminCtx()

			_, err := svc.CreateOperation(ctx, withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
				OperationType: "INSTALL", BundleId: "bundle-001", ReleaseDefinitionId: "def-001",
				ValuesRevisionId: "vr-001",
				SignatureRef:     tt.signature,
			}), "audit-"+strings.ReplaceAll(tt.name, " ", "-")))
			require.Error(t, err)
			assert.Equal(t, tt.wantCode, connect.CodeOf(err))
			require.NoError(t, emitter.Shutdown(context.Background()))

			events, listErr := st.AuditEvents().ListByResource(t.Context(), "release_bundle", "bundle-001")
			require.NoError(t, listErr)
			require.Len(t, events, 1)
			assert.Equal(t, "user-001", events[0].ActorID)
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
		ChartRef: "nginx", ChartDigest: "sha256:" + digest, CreatedAt: time.Now().UTC(),
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
		Version:             2,
		Status:              store.ValuesStatusDraft,
		CanonicalDocument:   []byte(`{}`),
		Digest:              "digest-vr-draft",
		ParentRevisionID:    "vr-001",
	}
	require.NoError(t, st.Values().Create(context.Background(), draft))

	_, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    draft.ID,
	}), "idem-draft"))
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

// ADR-009: operations left in preflight after a restart resume coordination,
// and shutdown drains the resumed runs before the store closes.
func TestResumePreflights_RestartsPreflightCoordination(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	// Active operator for the definition's cluster so stages can dispatch.
	require.NoError(t, st.Operators().Create(context.Background(), &store.Operator{
		ID: "operator-resume", Name: "operator-resume", CustomerID: "cust-001", ClusterID: "cls-001",
		CertSerial: "serial-resume", Status: store.OperatorActive,
	}))
	seedCancelableOperation(t, st, "op-resume", store.StatusPreflight)

	n, err := svc.ResumePreflights(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// The resumed coordinator starts the two-phase lifecycle (AC-019-05).
	require.Eventually(t, func() bool {
		pl, err := st.PreflightLifecycles().GetByOperationID(context.Background(), "op-resume")
		return err == nil && pl.Overall == "running"
	}, 5*time.Second, 50*time.Millisecond)

	// Shutdown cancels and drains the resumed run.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, svc.Shutdown(shutdownCtx))
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

// AC-019-03/07 wiring: cancelling an operation in preflight propagates to the
// running coordinator, which finalizes the lifecycle as cancelled.
func TestCancelOperation_PreflightPropagatesCancellation(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	// Active operator so the resumed preflight dispatches instead of failing
	// closed (production semantics).
	require.NoError(t, st.Operators().Create(context.Background(), &store.Operator{
		ID: "operator-cancel-preflight", Name: "operator-cancel-preflight", CustomerID: "cust-001", ClusterID: "cls-001",
		CertSerial: "serial-cancel-preflight", Status: store.OperatorActive,
	}))
	seedCancelableOperation(t, st, "op-cancel-preflight", store.StatusPreflight)

	n, err := svc.ResumePreflights(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// The coordinator is polling; cancel through the public handler.
	req := connect.NewRequest(&orchestratorv1.CancelOperationRequest{
		OperationId: "op-cancel-preflight", Reason: "cancel preflight", ExpectedStateVersion: 1,
	})
	req.Header().Set("Idempotency-Key", "cancel-preflight")
	resp, err := svc.CancelOperation(deployerCtx(), req)
	require.NoError(t, err)
	assert.Equal(t, orchestratorv1.OperationStatus_OPERATION_STATUS_CANCELLED, resp.Msg.Operation.State)

	// AC-019-07: the coordinator finalizes the lifecycle as cancelled.
	require.Eventually(t, func() bool {
		pl, err := st.PreflightLifecycles().GetByOperationID(context.Background(), "op-cancel-preflight")
		return err == nil && pl.Overall == "cancelled"
	}, 5*time.Second, 50*time.Millisecond)
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

// AC-067-02: an UPGRADE whose expected revision does not match the inventory
// is rejected with revision_conflict and no write (REQ-067 rule 13).
func TestCreateOperation_RevisionConflict(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedUpgradeInventory(t, st) // inventory revision = 1
	seedValuesRevision(t, st, "vr-revconf", "def-001", store.ValuesStatusApproved)

	req := upgradeRequest("vr-revconf")
	req.Msg.ExpectedCurrentRevision = 5 // does not match inventory revision 1
	_, err := svc.CreateOperation(adminCtx(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "revision_conflict")

	operations, listErr := st.Operations().List(context.Background(), "def-001")
	require.NoError(t, listErr)
	assert.Empty(t, operations, "revision conflict must not write an operation")
}

// AC-067-03: a bundle whose chart_ref does not match the definition chart_name
// is rejected with chart_mismatch.
func TestCreateOperation_ChartMismatch(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	require.NoError(t, st.Bundles().Create(t.Context(), &store.ReleaseBundle{
		ID: "bundle-chart-mismatch", Name: "wrong chart bundle", DigestAlg: "sha256",
		DigestValue: fmt.Sprintf("%064x", 91), Status: store.BundleValidated,
		ChartRef: "nginx-ingress", CreatedAt: time.Now().UTC(),
	}))

	_, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-chart-mismatch",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
	}), "idem-chart"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "chart_mismatch")
}

// AC-067-08: a bundle still in received state is not ready for release.
func TestCreateOperation_BundleNotReady(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	require.NoError(t, st.Bundles().Create(t.Context(), &store.ReleaseBundle{
		ID: "bundle-received", Name: "received bundle", DigestAlg: "sha256",
		DigestValue: fmt.Sprintf("%064x", 92), Status: store.BundleReceived,
		ChartRef: "nginx", CreatedAt: time.Now().UTC(),
	}))

	_, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-received",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
	}), "idem-received"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "bundle_not_ready")
}

// AC-067-09: a rejected bundle blocks creation.
func TestCreateOperation_BundleRejected(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	require.NoError(t, st.Bundles().Create(t.Context(), &store.ReleaseBundle{
		ID: "bundle-rejected", Name: "rejected bundle", DigestAlg: "sha256",
		DigestValue: fmt.Sprintf("%064x", 93), Status: store.BundleRejected,
		ChartRef: "nginx", CreatedAt: time.Now().UTC(),
	}))

	_, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-rejected",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
	}), "idem-rejected"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "bundle_rejected")
}

// AC-067-11: a deployer is not authorized to create standard operations
// (only release_admin / platform_admin are, REQ-067 rule 2).
func TestCreateOperation_DeployerDenied(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	require.NoError(t, st.Users().Create(context.Background(), &store.User{
		ID: "user-deployer", Username: "user-deployer", Status: store.UserActive,
	}))
	require.NoError(t, st.OrgMembers().Create(context.Background(), &store.OrganizationMember{
		OrgID: "org-001", UserID: "user-deployer", Role: store.RoleDeployer,
	}))

	deployerCtx := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-deployer", OrganizationID: "org-001", Roles: []string{string(store.RoleDeployer)},
	})
	_, err := svc.CreateOperation(deployerCtx, withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
	}), "idem-deployer"))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// AC-067-12: a values_patch containing a literal secret is rejected with
// secret_literal_forbidden (ADR-007, REQ-067 rule 14).
func TestCreateOperation_SecretPatchRejected(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	_, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
		ValuesPatch: &structpb.Struct{Fields: map[string]*structpb.Value{
			"password": structpb.NewStringValue("hunter2"),
		}},
	}), "idem-secret"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "secret_literal_forbidden")
}

// AC-067-13: when no operator is available for the preflight dispatch, the
// operation is still persisted in pending and the dispatch is durably queued.
func TestCreateOperation_CoordinatorUnavailablePersistsDispatch(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	// No operators registered: Coordinator.Dispatch returns errNoOperator,
	// which forces the deferred-dispatch path (REQ-067 rule: dispatch persists).
	resp, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
	}), "idem-noop"))
	require.NoError(t, err, "create operation must succeed even without an operator")
	stored, err := st.Operations().Get(context.Background(), resp.Msg.OperationId)
	require.NoError(t, err)
	// AC-067-13: the operation is durably written (pending at UOW commit; the
	// handler then synchronously advances it to preflight before responding).
	assert.False(t, stored.Status.IsTerminal(), "operation must be persisted, got %s", stored.Status)

	_, err = st.Outbox().GetByCommandID(context.Background(), resp.Msg.OperationId+":artifact")
	require.NoError(t, err, "preflight dispatch must be durably persisted")
}

// ── AC-077-04/13: effect_status projection matrix ──

// emergencyProjectionOp creates an EMERGENCY operation with an intent in the
// given delivery state and returns both rows.
func emergencyProjectionOp(t *testing.T, st store.Store, id, definitionID, deliveryStatus string, effect store.EmergencyEffectStatus) (*store.Operation, *store.EmergencyIntent) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	intent := &store.EmergencyIntent{
		ID: uuid.NewString(), ReleaseDefinitionID: definitionID, OperationID: id, CommandID: uuid.NewString(),
		Action: store.EmergencySetReplicas, WorkloadKind: "DEPLOYMENT", WorkloadName: "api", WorkloadNamespace: "default", WorkloadUID: "uid-" + id,
		Convergence: store.EmergencyRevertOnNextReconcile, DeliveryStatus: deliveryStatus,
		EffectStatus: effect, CreatedAt: now, UpdatedAt: now,
	}
	op := &store.Operation{
		ID: id, OperationType: store.OperationEmergency, Status: store.StatusPending,
		ReleaseDefinitionID: definitionID, IdempotencyKey: id + "-key", RequestHash: id + "-hash",
		CreatedAt: now, UpdatedAt: now,
	}
	command := store.EmergencyCreateCommand{
		Operation: op, Intent: intent,
		IdempotencyScope: "org-001:" + definitionID, IdempotencyKeyHash: id + "-hash",
		RequestHash: id + "-hash", IdempotencyExpiresAt: now.Add(time.Hour),
	}
	uowStore, ok := st.(interface {
		store.Store
		OperationCreationUnitOfWork() store.OperationCreationUnitOfWork
	})
	require.True(t, ok)
	_, err := uowStore.OperationCreationUnitOfWork()(ctx, store.OperationCreationRequest{
		Operation: op,
		Emergency: &command,
	})
	require.NoError(t, err)
	return op, intent
}

func TestGetOperation_EffectStatusProjectionMatrix(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	for _, defID := range []string{"def-002", "def-003", "def-004", "def-005", "def-006", "def-007"} {
		require.NoError(t, st.Definitions().Create(context.Background(), &store.ReleaseDefinition{
			ID: defID, Name: defID, CustomerID: "cust-001", ClusterID: "clus-001",
			Namespace: "apps", ReleaseName: defID, Status: store.DefStatusActive,
		}, nil))
	}

	// Non-EMERGENCY operation always projects NOT_STARTED (AC-077-04).
	require.NoError(t, st.Operations().Create(context.Background(), &store.Operation{
		ID: "op-standard-proj", OperationType: store.OperationInstall,
		Status: store.StatusRunning, ReleaseDefinitionID: "def-001",
		IdempotencyKey: "std-proj-key", RequestHash: "std-proj-hash", StateVersion: 1,
	}))

	// EMERGENCY matrix (AC-077-13): pending/queued → NOT_STARTED;
	// delivered/persisted with UNKNOWN effect → UNKNOWN; resolved → APPLIED/NOT_APPLIED.
	// One active EMERGENCY per definition (conflict rule).
	opPending, _ := emergencyProjectionOp(t, st, "op-emo-pending", "def-002", "pending", store.EmergencyEffectUnknown)
	opQueued, _ := emergencyProjectionOp(t, st, "op-emo-queued", "def-003", "queued", store.EmergencyEffectUnknown)
	opDelivered, _ := emergencyProjectionOp(t, st, "op-emo-delivered", "def-004", "delivered", store.EmergencyEffectUnknown)
	opPersisted, _ := emergencyProjectionOp(t, st, "op-emo-persisted", "def-005", "persisted", store.EmergencyEffectUnknown)
	opResolvedApplied, _ := emergencyProjectionOp(t, st, "op-emo-applied", "def-006", "persisted", store.EmergencyEffectApplied)
	opResolvedNotApplied, _ := emergencyProjectionOp(t, st, "op-emo-not-applied", "def-007", "persisted", store.EmergencyEffectNotApplied)

	tests := []struct {
		name     string
		opID     string
		expected orchestratorv1.EmergencyEffectStatus
	}{
		{"non-emergency", "op-standard-proj", orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_NOT_STARTED},
		{"emergency pending", opPending.ID, orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_NOT_STARTED},
		{"emergency queued", opQueued.ID, orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_NOT_STARTED},
		{"emergency delivered unknown", opDelivered.ID, orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_UNKNOWN},
		{"emergency persisted unknown", opPersisted.ID, orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_UNKNOWN},
		{"emergency applied", opResolvedApplied.ID, orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_APPLIED},
		{"emergency not applied", opResolvedNotApplied.ID, orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_NOT_APPLIED},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := svc.GetOperation(deployerCtx(), connect.NewRequest(&orchestratorv1.GetOperationRequest{
				OperationId: tt.opID,
			}))
			require.NoError(t, err)
			assert.Equal(t, tt.expected, resp.Msg.Operation.EffectStatus)
		})
	}
}

// AC-077-08 projection: after a late result resolves the effect, a subsequent
// GetOperation must project the new value (existing ResolveEmergencyEffect path).
func TestGetOperation_EffectStatusReflectsLateResolution(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	require.NoError(t, st.Definitions().Create(context.Background(), &store.ReleaseDefinition{
		ID: "def-late-resolve", Name: "def-late-resolve", CustomerID: "cust-001", ClusterID: "clus-001",
		Namespace: "apps", ReleaseName: "late-resolve", Status: store.DefStatusActive,
	}, nil))

	op, intent := emergencyProjectionOp(t, st, "op-emo-late-resolve", "def-late-resolve", "delivered", store.EmergencyEffectUnknown)
	ctx := context.Background()
	op, err := st.Operations().UpdateStatus(ctx, op.ID, store.StatusQueued, op.StateVersion, "")
	require.NoError(t, err)
	op, err = st.Operations().UpdateStatus(ctx, op.ID, store.StatusRunning, op.StateVersion, "")
	require.NoError(t, err)
	finished, err := st.EmergencyIntents().Finish(
		ctx, intent.ID, op.ID, op.StateVersion, store.StatusFailed,
		store.EmergencyEffectUnknown, "execution_error", nil, nil,
	)
	require.NoError(t, err)
	_, err = st.EmergencyIntents().ResolveEmergencyEffect(ctx, store.ResolveEmergencyEffectCommand{
		OperationID:          op.ID,
		ExpectedStateVersion: finished.StateVersion,
		EffectStatus:         store.EmergencyEffectApplied,
		RequestID:            "late-resolve",
	})
	require.NoError(t, err)

	resp, err := svc.GetOperation(deployerCtx(), connect.NewRequest(&orchestratorv1.GetOperationRequest{
		OperationId: op.ID,
	}))
	require.NoError(t, err)
	assert.Equal(t, orchestratorv1.EmergencyEffectStatus_EMERGENCY_EFFECT_STATUS_APPLIED, resp.Msg.Operation.EffectStatus)
}

// ── AC-077-05/14: cursor_expired carries a decodable snapshot ──

func TestCursorExpiredErrorSnapshotProto(t *testing.T) {
	op := &store.Operation{
		ID: "op-cursor-expired", OperationType: store.OperationInstall,
		Status: store.StatusSucceeded, ReleaseDefinitionID: "def-001",
		IdempotencyKey: "cursor-ik", RequestHash: "cursor-rh", StateVersion: 4,
	}
	snapshot := &store.TimelineSnapshot{
		Operation: op, SnapshotSequence: 7, RetainedFromSequence: 3,
	}
	err := operationCursorExpiredError(snapshot)
	require.Error(t, err)
	assert.Equal(t, connect.CodeOutOfRange, connect.CodeOf(err))

	// Header values are read from the error's Meta.
	assert.Equal(t, "cursor_expired", connectErrMeta(err, "X-Reason-Code"))
	assert.Equal(t, "3", connectErrMeta(err, "X-Retained-From-Sequence"))
	encoded := connectErrMeta(err, "X-Snapshot-Proto")
	require.NotEmpty(t, encoded)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	var decoded orchestratorv1.OperationSnapshot
	require.NoError(t, protojson.Unmarshal(raw, &decoded))
	assert.Equal(t, int64(7), decoded.SnapshotSequence)
	assert.Equal(t, int64(3), decoded.RetainedFromSequence)
	assert.Equal(t, "op-cursor-expired", decoded.Operation.GetOperationId())
}

func connectErrMeta(err error, key string) string {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return ""
	}
	return connectErr.Meta().Get(key)
}

// ── AC-077-06: heartbeat hard limit ≤10s (implementation value 5s) ──

func TestOperationWatchHeartbeatWithinHardLimit(t *testing.T) {
	assert.LessOrEqual(t, operationWatchHeartbeat, 10*time.Second)
	assert.Equal(t, 5*time.Second, operationWatchHeartbeat)
}

// ── AC-077-01/02/03 serialization cases ──

func TestToProtoTimelineEntryNewKinds(t *testing.T) {
	now := time.Now().UTC()
	ackData, err := json.Marshal(store.AckTimelineData{RequestID: "req-ack", AckStage: "persisted"})
	require.NoError(t, err)
	ack := toProtoTimelineEntry(&store.OperationTimelineEntry{
		ID: "e-ack", OperationID: "op-1", Sequence: 1, Kind: string(store.TimelineEntryACK),
		OperationStateVersion: 2, Data: ackData, CreatedAt: now,
	})
	assert.Equal(t, orchestratorv1.TimelineEntryKind_TIMELINE_ENTRY_KIND_ACK, ack.Kind)
	assert.Equal(t, "req-ack", ack.RequestId)
	assert.Equal(t, "persisted", ack.AckStage)

	progressData, err := json.Marshal(store.RolloutProgressTimelineData{WorkloadRef: "deployments/app/default", Ready: 2, Desired: 3})
	require.NoError(t, err)
	progress := toProtoTimelineEntry(&store.OperationTimelineEntry{
		ID: "e-progress", OperationID: "op-1", Sequence: 2, Kind: string(store.TimelineEntryRolloutProgress),
		OperationStateVersion: 2, Data: progressData, CreatedAt: now,
	})
	assert.Equal(t, orchestratorv1.TimelineEntryKind_TIMELINE_ENTRY_KIND_ROLLOUT_PROGRESS, progress.Kind)
	assert.Equal(t, "deployments/app/default", progress.WorkloadRef)
	assert.Equal(t, int32(2), progress.Ready)
	assert.Equal(t, int32(3), progress.Desired)

	errorData, err := json.Marshal(store.ErrorTimelineData{RequestID: "req-err", ErrorCode: "helm_upgrade_failed", ErrorMessage: "sanitized"})
	require.NoError(t, err)
	errorEntry := toProtoTimelineEntry(&store.OperationTimelineEntry{
		ID: "e-error", OperationID: "op-1", Sequence: 3, Kind: string(store.TimelineEntryError),
		OperationStateVersion: 2, Data: errorData, CreatedAt: now,
	})
	assert.Equal(t, orchestratorv1.TimelineEntryKind_TIMELINE_ENTRY_KIND_ERROR, errorEntry.Kind)
	assert.Equal(t, "req-err", errorEntry.RequestId)
	assert.Equal(t, "helm_upgrade_failed", errorEntry.ErrorCode)
	assert.Equal(t, "sanitized", errorEntry.ErrorMessage)
}
