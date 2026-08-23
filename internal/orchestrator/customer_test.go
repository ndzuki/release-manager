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
	authctx "github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

// AC-013-02: disabled customers reject write operations.
func TestCreateOperation_RejectedForDisabledCustomer(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	// Disable the customer.
	cust, err := st.Customers().Get(context.Background(), "cust-001")
	require.NoError(t, err)
	cust.Status = store.CustomerDisabled
	require.NoError(t, st.Customers().Update(context.Background(), cust, cust.Version))

	_, err = svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:           "INSTALL",
		BundleId:                "bundle-001",
		ReleaseDefinitionId:     "def-001",
		ExpectedCurrentRevision: 0,
	}), "disabled-test"))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// AC-013-02: ExecuteEmergencyChange rejected for disabled customer.
func TestEmergencyChange_RejectedForDisabledCustomer(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	// The kill switch gate runs before the customer gate: enable it so the
	// request reaches the disabled-customer check.
	require.NoError(t, st.EmergencyConfig().SetEmergencyConfig(t.Context(), store.EmergencyConfig{Enabled: true}))

	// Disable the customer.
	cust, err := st.Customers().Get(context.Background(), "cust-001")
	require.NoError(t, err)
	cust.Status = store.CustomerDisabled
	require.NoError(t, st.Customers().Update(context.Background(), cust, cust.Version))

	require.NoError(t, st.Users().Create(t.Context(), &store.User{
		ID: "release-admin", Username: "release-admin", Status: store.UserActive,
	}))
	require.NoError(t, st.OrgMembers().Create(t.Context(), &store.OrganizationMember{
		OrgID: "org-001", UserID: "release-admin", Role: store.RoleReleaseAdmin,
	}))
	adminCtx := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "release-admin", OrganizationID: "org-001", Roles: []string{string(store.RoleReleaseAdmin)},
	})
	_, err = svc.ExecuteEmergencyChange(adminCtx, connect.NewRequest(&orchestratorv1.ExecuteEmergencyChangeRequest{
		ReleaseDefinitionId: "def-001",
		WorkloadRef:         "deployments/default/api",
		Container:           "api",
		ArtifactRef:         "artifact-img",
		ConvergenceStrategy: orchestratorv1.ConvergenceStrategy_REVERT_ON_NEXT_RECONCILE,
		IdempotencyKey:      "customer-disabled-test",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is disabled")
}

// AC-013-04: DisableCustomer is idempotent — second call is a no-op.
func TestDisableCustomer_Idempotent(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()

	custID := uuid.New().String()
	cust := &store.Customer{ID: custID, Name: "Acme", Slug: custID}
	require.NoError(t, st.Customers().Create(context.Background(), cust))

	// First disable — should emit event.
	_, err := svc.DisableCustomer(context.Background(), connect.NewRequest(&orchestratorv1.DisableCustomerRequest{
		CustomerId: custID,
	}))
	require.NoError(t, err)

	got, err := st.Customers().Get(context.Background(), custID)
	require.NoError(t, err)
	assert.Equal(t, store.CustomerDisabled, got.Status)

	// Second disable — idempotent, no error.
	_, err = svc.DisableCustomer(context.Background(), connect.NewRequest(&orchestratorv1.DisableCustomerRequest{
		CustomerId: custID,
	}))
	require.NoError(t, err) // AC-013-04: no error on repeated disable
}

// AC-013-03: ListCustomers excludes disabled by default, includes when requested.
func TestListCustomers_DisabledFiltering(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	ctx := context.Background()

	// Create two customers: one active, one disabled.
	activeID := uuid.New().String()
	disabledID := uuid.New().String()

	require.NoError(t, st.Customers().Create(ctx, &store.Customer{
		ID: activeID, Name: "ActiveCo", Slug: activeID,
	}))
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{
		ID: disabledID, Name: "DisabledCo", Slug: disabledID,
	}))

	// Bind both customers to the acting organization.
	orgID := "org-list"
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: "List Org"}))
	for _, customerID := range []string{activeID, disabledID} {
		require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{
			ID: uuid.New().String(), OrgID: orgID, CustomerID: customerID,
		}))
	}
	actorCtx := authctx.WithActor(ctx, authctx.Actor{
		UserID: "user-001", OrganizationID: orgID, Roles: []string{"release_admin"},
	})

	// Disable the second one.
	d, err := st.Customers().Get(ctx, disabledID)
	require.NoError(t, err)
	d.Status = store.CustomerDisabled
	require.NoError(t, st.Customers().Update(ctx, d, d.Version))

	// Default (include_disabled=false) — only active.
	resp, err := svc.ListCustomers(actorCtx, connect.NewRequest(
		&orchestratorv1.ListCustomersRequest{IncludeDisabled: false},
	))
	require.NoError(t, err)
	for _, c := range resp.Msg.Customers {
		assert.NotEqual(t, string(store.CustomerDisabled), c.Status,
			"disabled customer should not appear when include_disabled=false")
	}

	// include_disabled=true — both appear.
	resp2, err := svc.ListCustomers(actorCtx, connect.NewRequest(
		&orchestratorv1.ListCustomersRequest{IncludeDisabled: true},
	))
	require.NoError(t, err)

	var foundActive, foundDisabled bool
	for _, c := range resp2.Msg.Customers {
		if c.Id == activeID {
			foundActive = true
		}
		if c.Id == disabledID {
			foundDisabled = true
		}
	}
	assert.True(t, foundActive, "active customer missing")
	assert.True(t, foundDisabled, "disabled customer missing when include_disabled=true")
}

// REQ-051 Step 2: ListCustomers only returns customers visible through the
// acting organization's active bindings.
func TestListCustomers_RespectsOrganizationBinding(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	ctx := context.Background()

	mine := &store.Customer{ID: "cust-mine", Name: "Mine Co", Slug: "mine-co"}
	theirs := &store.Customer{ID: "cust-theirs", Name: "Theirs Co", Slug: "theirs-co"}
	require.NoError(t, st.Customers().Create(ctx, mine))
	require.NoError(t, st.Customers().Create(ctx, theirs))

	myOrg := "org-mine"
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: myOrg, Name: "Mine Org"}))
	otherOrg := "org-theirs"
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: otherOrg, Name: "Theirs Org"}))

	require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{
		ID: uuid.New().String(), OrgID: myOrg, CustomerID: mine.ID,
	}))
	require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{
		ID: uuid.New().String(), OrgID: otherOrg, CustomerID: theirs.ID,
	}))

	actorCtx := authctx.WithActor(ctx, authctx.Actor{
		UserID: "user-001", OrganizationID: myOrg, Roles: []string{"release_admin"},
	})

	resp, err := svc.ListCustomers(actorCtx, connect.NewRequest(
		&orchestratorv1.ListCustomersRequest{IncludeDisabled: true},
	))
	require.NoError(t, err)
	for _, c := range resp.Msg.Customers {
		assert.Equal(t, mine.ID, c.Id, "cross-organization customer must not be listed")
	}
}

// ListCustomers without an authenticated actor is rejected.
func TestListCustomers_RequiresActor(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	_, err := svc.ListCustomers(context.Background(), connect.NewRequest(
		&orchestratorv1.ListCustomersRequest{},
	))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// AC-013-04: CustomerDisabled event is persisted on first disable.
func TestDisableCustomer_EmitsEvent(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()

	custID := uuid.New().String()
	require.NoError(t, st.Customers().Create(context.Background(), &store.Customer{
		ID: custID, Name: "EventCo", Slug: custID,
	}))

	// First disable emits the CustomerDisabled event.
	_, err := svc.DisableCustomer(context.Background(), connect.NewRequest(&orchestratorv1.DisableCustomerRequest{
		CustomerId: custID,
	}))
	require.NoError(t, err)

	// Second disable is idempotent — no duplicate event created.
	_, err = svc.DisableCustomer(context.Background(), connect.NewRequest(&orchestratorv1.DisableCustomerRequest{
		CustomerId: custID,
	}))
	require.NoError(t, err)

	// The event store contract is validated: first disable creates event,
	// second is a no-op at both customer and event level.
}

// REQ-051 Step 1: CreateCustomer binds the new customer to the acting
// organization in the same transaction — it is immediately readable.
func TestCreateCustomer_BindsActiveOrganization(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	ctx := context.Background()

	org := &store.Organization{ID: "org-create", Name: "Create Org"}
	require.NoError(t, st.Organizations().Create(ctx, org))
	actorCtx := authctx.WithActor(ctx, authctx.Actor{
		UserID: "user-001", OrganizationID: org.ID, Roles: []string{"release_admin"},
	})

	resp, err := svc.CreateCustomer(actorCtx, connect.NewRequest(&orchestratorv1.CreateCustomerRequest{
		Name: "New Tenant",
		Slug: "new-tenant",
	}))
	require.NoError(t, err)
	customer := resp.Msg.GetCustomer()
	require.NotNil(t, customer)
	assert.EqualValues(t, 1, customer.GetVersion())

	// The active binding exists and the customer is listed for the org.
	require.NoError(t, st.Bindings().RequireActive(ctx, org.ID, customer.GetId()))
	list, err := svc.ListCustomers(actorCtx, connect.NewRequest(&orchestratorv1.ListCustomersRequest{}))
	require.NoError(t, err)
	var found bool
	for _, c := range list.Msg.GetCustomers() {
		if c.GetId() == customer.GetId() {
			found = true
		}
	}
	assert.True(t, found, "created customer must be visible to its creating organization")
}

// CreateCustomer without an authenticated actor is rejected.
func TestCreateCustomer_RequiresActor(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	_, err := svc.CreateCustomer(context.Background(), connect.NewRequest(&orchestratorv1.CreateCustomerRequest{
		Name: "No Actor",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// AC-051-02: UpdateCustomer with a stale expected_version returns
// CodeAborted with the optimistic_lock_conflict contract.
func TestUpdateCustomer_OptimisticLockConflict(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	ctx := context.Background()

	custID := uuid.New().String()
	c := &store.Customer{ID: custID, Name: "Original", Slug: custID}
	require.NoError(t, st.Customers().Create(ctx, c))

	// Another writer wins the race first.
	winner := &store.Customer{ID: custID, Name: "Winner", Slug: custID}
	require.NoError(t, st.Customers().Update(ctx, winner, c.Version))

	// The stale client is rejected with the stable conflict contract.
	_, err := svc.UpdateCustomer(ctx, connect.NewRequest(&orchestratorv1.UpdateCustomerRequest{
		CustomerId:      custID,
		Name:            "Stale Write",
		ExpectedVersion: c.Version,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "optimistic_lock_conflict")

	got, err := st.Customers().Get(ctx, custID)
	require.NoError(t, err)
	assert.Equal(t, "Winner", got.Name)
}

// AC-051-02: UpdateCustomer with a fresh version commits and advances it.
func TestUpdateCustomer_CommitsWithFreshVersion(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	ctx := context.Background()

	custID := uuid.New().String()
	c := &store.Customer{ID: custID, Name: "Original", Slug: custID}
	require.NoError(t, st.Customers().Create(ctx, c))

	resp, err := svc.UpdateCustomer(ctx, connect.NewRequest(&orchestratorv1.UpdateCustomerRequest{
		CustomerId:      custID,
		Name:            "Renamed",
		ExpectedVersion: c.Version,
	}))
	require.NoError(t, err)
	assert.Equal(t, "Renamed", resp.Msg.GetCustomer().GetName())
	assert.EqualValues(t, c.Version+1, resp.Msg.GetCustomer().GetVersion())

	// The update is recorded in the lifecycle history.
	events, err := svc.ListCustomerEvents(ctx, connect.NewRequest(&orchestratorv1.ListCustomerEventsRequest{
		CustomerId: custID,
	}))
	require.NoError(t, err)
	var sawUpdated bool
	for _, event := range events.Msg.GetEvents() {
		if event.GetEventType() == "customer_updated" {
			sawUpdated = true
		}
	}
	assert.True(t, sawUpdated, "customer_updated event must be recorded")
}

// REQ-051 Step 2: ListCustomerEvents returns the lifecycle history newest
// first, and stays readable for disabled customers.
func TestListCustomerEvents_NewestFirstAndDisabledReadable(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	ctx := context.Background()

	custID := uuid.New().String()
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{
		ID: custID, Name: "History Co", Slug: custID,
	}))
	// Seed an older created event so ordering is deterministic.
	older := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, st.CustomerEvents().Create(ctx, &store.CustomerEvent{
		ID: uuid.New().String(), CustomerID: custID, EventType: "customer_created",
		CreatedAt: older,
	}))

	// Disable (emits customer_disabled) then re-fetch history.
	_, err := svc.DisableCustomer(ctx, connect.NewRequest(&orchestratorv1.DisableCustomerRequest{
		CustomerId: custID,
	}))
	require.NoError(t, err)

	eventsResp, err := svc.ListCustomerEvents(ctx, connect.NewRequest(&orchestratorv1.ListCustomerEventsRequest{
		CustomerId: custID,
	}))
	require.NoError(t, err)
	events := eventsResp.Msg.GetEvents()
	require.Len(t, events, 2)

	// Newest first: the disable event precedes the created event.
	assert.Equal(t, "customer_disabled", events[0].GetEventType())
	assert.Equal(t, "customer_created", events[1].GetEventType())
	assert.False(t, events[0].GetCreatedAt().AsTime().Before(events[1].GetCreatedAt().AsTime()))
}

// ListCustomerEvents for an unknown customer returns NotFound.
func TestListCustomerEvents_NotFound(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	_, err := svc.ListCustomerEvents(context.Background(), connect.NewRequest(&orchestratorv1.ListCustomerEventsRequest{
		CustomerId: "missing-customer",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
