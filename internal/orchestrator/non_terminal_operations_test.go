package orchestrator

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/contracts"
	"github.com/ndzuki/release-manager/internal/store"
)

// seedBoundDefinition creates one active release definition for an existing
// customer. Multiple definitions under one customer let the pagination tests
// respect the PostgreSQL invariant of at most one active standard operation per
// definition.
func seedBoundDefinition(t *testing.T, st store.Store, id, name, customerID string) {
	t.Helper()
	require.NoError(t, st.Definitions().Create(t.Context(), &store.ReleaseDefinition{
		ID: id, Name: name, CustomerID: customerID, ClusterID: "cls-001",
		Namespace: "default", ReleaseName: id, Status: store.DefStatusActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, nil))
}

// seedOperationAt inserts one operation clocked at createdAt so the feed's
// oldest-first ordering is deterministic; ids and idempotency keys must be
// unique per case.
func seedOperationAt(
	t *testing.T,
	st store.Store,
	id, definitionID string,
	opType store.OperationType,
	status store.OperationStatus,
	createdAt time.Time,
) {
	t.Helper()
	require.NoError(t, st.Operations().Create(t.Context(), &store.Operation{
		ID: id, OperationType: opType, Status: status,
		ReleaseDefinitionID: definitionID, IdempotencyKey: uuid.NewString(),
		RequestHash: id + "-hash", ExpectedRevision: 1,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}))
}

// TestListNonTerminalOperationsScopesToActiveBindings is the tenancy gate for
// the aggregate feed (TASK-276, AC-276-04): an operation that is non-terminal
// but belongs to a definition the caller's organization has no active binding
// with must be invisible, not merely filtered by status.
// The name says "bound customers" deliberately: this fixture binds the caller to some
// customers and not others, but it holds no REVOKED binding, so the handler's
// `Status == BindingActive` filter is not covered here (removing it leaves this test
// green). Coverage for a revoked binding is tracked as a follow-up; the scoping
// predicate itself is falsifiable -- dropping it leaks op-hidden and fails this test.
func TestListNonTerminalOperationsScopesToBoundCustomers(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	// A second customer the caller's organization IS bound to (cust-002) and a
	// third it is NOT (cust-unbound).
	require.NoError(t, createCustomerViaManagement(t.Context(), st, &store.Customer{
		ID: "cust-002", Name: "Second Customer", Slug: "second-customer",
	}))
	require.NoError(t, st.Bindings().Create(t.Context(), &store.OrgCustomerBinding{
		ID: "binding-002", OrgID: "org-001", CustomerID: "cust-002",
	}))
	require.NoError(t, createCustomerViaManagement(t.Context(), st, &store.Customer{
		ID: "cust-unbound", Name: "Unbound", Slug: "unbound",
	}))
	seedBoundDefinition(t, st, "def-002", "second-release", "cust-002")
	seedBoundDefinition(t, st, "def-unbound", "unbound", "cust-unbound")

	base := time.Date(2026, time.October, 10, 1, 0, 0, 0, time.UTC)
	seedOperationAt(t, st, "op-visible", "def-001", store.OperationUpgrade, store.StatusRunning, base)
	seedOperationAt(t, st, "op-second", "def-002", store.OperationInstall, store.StatusQueued, base.Add(time.Minute))
	seedOperationAt(t, st, "op-hidden", "def-unbound", store.OperationInstall, store.StatusRunning, base.Add(2*time.Minute))
	// Terminal operations are excluded by definition, not by scope.
	seedOperationAt(t, st, "op-terminal", "def-001", store.OperationInstall, store.StatusSucceeded, base.Add(-time.Minute))

	resp, err := svc.ListNonTerminalOperations(deployerCtx(), connect.NewRequest(
		&orchestratorv1.ListNonTerminalOperationsRequest{}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetOperations(), 2, "only the two bound customers' operations are visible")

	first := resp.Msg.GetOperations()[0]
	assert.Equal(t, "op-visible", first.GetOperationId())
	assert.Equal(t, "def-001", first.GetReleaseDefinitionId())
	assert.Equal(t, "my-release", first.GetReleaseDefinitionName())
	assert.Equal(t, "cust-001", first.GetCustomerId())
	assert.Equal(t, "Test Customer", first.GetCustomerName())
	assert.Equal(t, string(store.OperationUpgrade), first.GetOperationType())
	assert.Equal(t, string(store.StatusRunning), first.GetState())
	assert.Equal(t, base, first.GetCreatedAt().AsTime())
	assert.Equal(t, base, first.GetUpdatedAt().AsTime())
	assert.False(t, first.GetEmergency())
	assert.Equal(t, int32(1), first.GetRevision())

	second := resp.Msg.GetOperations()[1]
	assert.Equal(t, "op-second", second.GetOperationId())
	assert.Equal(t, "second-release", second.GetReleaseDefinitionName())
	assert.Equal(t, "Second Customer", second.GetCustomerName())
	for _, row := range resp.Msg.GetOperations() {
		assert.NotEqual(t, "op-hidden", row.GetOperationId(), "an unbound customer's operation leaked into the page")
		assert.NotEqual(t, "op-terminal", row.GetOperationId(), "a terminal operation is not part of the feed")
	}

	// The hidden row is genuinely non-terminal: the unscoped enumeration (the
	// recovery/emergency feed this RPC must not expose) still sees it, so the
	// exclusion above is the tenant scope at work, not a missing fixture.
	unscoped, err := st.Operations().ListNonTerminal(t.Context())
	require.NoError(t, err)
	unscopedIDs := make([]string, 0, len(unscoped))
	for _, operation := range unscoped {
		unscopedIDs = append(unscopedIDs, operation.ID)
	}
	assert.Contains(t, unscopedIDs, "op-hidden")
	assert.Contains(t, unscopedIDs, "op-visible")
	assert.NotContains(t, unscopedIDs, "op-terminal")

	// The optional customer_id narrows a scope the caller already holds.
	narrowed, err := svc.ListNonTerminalOperations(deployerCtx(), connect.NewRequest(
		&orchestratorv1.ListNonTerminalOperationsRequest{CustomerId: "cust-002"}))
	require.NoError(t, err)
	require.Len(t, narrowed.Msg.GetOperations(), 1)
	assert.Equal(t, "op-second", narrowed.Msg.GetOperations()[0].GetOperationId())

	// And naming a customer outside the scope is refused, not silently empty.
	_, err = svc.ListNonTerminalOperations(deployerCtx(), connect.NewRequest(
		&orchestratorv1.ListNonTerminalOperationsRequest{CustomerId: "cust-unbound"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Equal(t, "binding_revoked", connectErrorReason(err))
}

// TestListNonTerminalOperationsPaginatesOldestFirst walks the cursor across a
// page boundary that falls between two rows sharing one created_at. The (created_at,
// id) tiebreaker is what keeps the keyset from skipping or repeating them.
func TestListNonTerminalOperationsPaginatesOldestFirst(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedBoundDefinition(t, st, "def-002", "second-release", "cust-001")
	seedBoundDefinition(t, st, "def-003", "third-release", "cust-001")

	base := time.Date(2026, time.October, 10, 2, 0, 0, 0, time.UTC)
	seedOperationAt(t, st, "op-01", "def-001", store.OperationInstall, store.StatusPending, base)
	seedOperationAt(t, st, "op-02", "def-002", store.OperationUpgrade, store.StatusQueued, base.Add(time.Minute))
	seedOperationAt(t, st, "op-03", "def-003", store.OperationRollback, store.StatusRunning, base.Add(time.Minute))
	seedOperationAt(t, st, "op-04", "def-001", store.OperationEmergency, store.StatusCancelling, base.Add(2*time.Minute))

	var ids []string
	var emergencies []bool
	const pageSize = 2
	token := ""
	for page := 0; ; page++ {
		require.Less(t, page, 5, "pagination did not terminate")
		resp, err := svc.ListNonTerminalOperations(deployerCtx(), connect.NewRequest(
			&orchestratorv1.ListNonTerminalOperationsRequest{PageSize: pageSize, PageToken: token}))
		require.NoError(t, err)
		assert.LessOrEqual(t, len(resp.Msg.GetOperations()), pageSize)
		for _, row := range resp.Msg.GetOperations() {
			ids = append(ids, row.GetOperationId())
			emergencies = append(emergencies, row.GetEmergency())
		}
		token = resp.Msg.GetNextPageToken()
		if page == 0 {
			require.NotEmpty(t, token, "the first page must announce the second one")
			cursorTime, cursorID, err := contracts.DecodeCursor(token)
			require.NoError(t, err)
			assert.Equal(t, "op-02", cursorID)
			assert.Equal(t, base.Add(time.Minute), cursorTime)
		}
		if token == "" {
			break
		}
	}
	assert.Equal(t, []string{"op-01", "op-02", "op-03", "op-04"}, ids, "oldest first, no repeats, no gaps")
	assert.Equal(t, []bool{false, false, false, true}, emergencies, "only the EMERGENCY change is flagged")
	assert.Empty(t, token, "the last page carries no next_page_token")
}

// TestListNonTerminalOperationsValidatesRequest pins the request contract:
// negative page_size and malformed page_token are INVALID_ARGUMENT, an unbound
// customer_id is PERMISSION_DENIED, and no actor is UNAUTHENTICATED.
func TestListNonTerminalOperationsValidatesRequest(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	tests := []struct {
		name     string
		ctx      context.Context
		request  *orchestratorv1.ListNonTerminalOperationsRequest
		wantCode connect.Code
		wantReas string
	}{
		{
			name: "negative page size", ctx: deployerCtx(),
			request:  &orchestratorv1.ListNonTerminalOperationsRequest{PageSize: -1},
			wantCode: connect.CodeInvalidArgument, wantReas: "invalid_page_size",
		},
		{
			name: "malformed page token", ctx: deployerCtx(),
			request:  &orchestratorv1.ListNonTerminalOperationsRequest{PageToken: "not-a-token"},
			wantCode: connect.CodeInvalidArgument, wantReas: "invalid_page_token",
		},
		{
			name: "customer without active binding", ctx: deployerCtx(),
			request:  &orchestratorv1.ListNonTerminalOperationsRequest{CustomerId: "cust-missing"},
			wantCode: connect.CodePermissionDenied, wantReas: "binding_revoked",
		},
		{
			name: "missing actor", ctx: context.Background(),
			request:  &orchestratorv1.ListNonTerminalOperationsRequest{},
			wantCode: connect.CodeUnauthenticated, wantReas: "authentication_required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.ListNonTerminalOperations(tt.ctx, connect.NewRequest(tt.request))
			require.Error(t, err)
			assert.Equal(t, tt.wantCode, connect.CodeOf(err))
			assert.Equal(t, tt.wantReas, connectErrorReason(err))
		})
	}
}

// TestParseListNonTerminalOperationsQueryClampsPageSize pins the page-size
// contract without seeding 100 rows: the default is 20 and an oversized value is
// clamped, not rejected (the shared contracts.NormalizePageSize behaviour
// ListOperations already has).
func TestParseListNonTerminalOperationsQueryClampsPageSize(t *testing.T) {
	query, err := parseListNonTerminalOperationsQuery(&orchestratorv1.ListNonTerminalOperationsRequest{})
	require.NoError(t, err)
	assert.Equal(t, 20, query.pageSize)

	query, err = parseListNonTerminalOperationsQuery(&orchestratorv1.ListNonTerminalOperationsRequest{PageSize: 1000})
	require.NoError(t, err)
	assert.Equal(t, 100, query.pageSize)
	assert.False(t, query.hasCursor)
}
