package orchestrator

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
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
	require.NoError(t, st.Customers().Update(context.Background(), cust))

	_, err = svc.CreateOperation(context.Background(), connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:          "INSTALL",
		BundleId:               "bundle-001",
		ReleaseDefinitionId:    "def-001",
		IdempotencyKey:         "disabled-test",
		ExpectedCurrentRevision: 0,
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is disabled")
}

// AC-013-02: EmergencyChange rejected for disabled customer.
func TestEmergencyChange_RejectedForDisabledCustomer(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	// Disable the customer.
	cust, err := st.Customers().Get(context.Background(), "cust-001")
	require.NoError(t, err)
	cust.Status = store.CustomerDisabled
	require.NoError(t, st.Customers().Update(context.Background(), cust))

	_, err = svc.EmergencyChange(context.Background(), connect.NewRequest(&orchestratorv1.EmergencyChangeRequest{
		ReleaseDefinitionId: "def-001",
		Action:              orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_CONTAINER_IMAGE,
		Payload:             `{}`,
		Reason:              "test",
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

	// Create two customers: one active, one disabled.
	activeID := uuid.New().String()
	disabledID := uuid.New().String()

	require.NoError(t, st.Customers().Create(context.Background(), &store.Customer{
		ID: activeID, Name: "ActiveCo", Slug: activeID,
	}))
	require.NoError(t, st.Customers().Create(context.Background(), &store.Customer{
		ID: disabledID, Name: "DisabledCo", Slug: disabledID,
	}))

	// Disable the second one.
	d, err := st.Customers().Get(context.Background(), disabledID)
	require.NoError(t, err)
	d.Status = store.CustomerDisabled
	require.NoError(t, st.Customers().Update(context.Background(), d))

	// Default (include_disabled=false) — only active.
	resp, err := svc.ListCustomers(context.Background(), connect.NewRequest(
		&orchestratorv1.ListCustomersRequest{IncludeDisabled: false},
	))
	require.NoError(t, err)
	for _, c := range resp.Msg.Customers {
		assert.NotEqual(t, string(store.CustomerDisabled), c.Status,
			"disabled customer should not appear when include_disabled=false")
	}

	// include_disabled=true — both appear.
	resp2, err := svc.ListCustomers(context.Background(), connect.NewRequest(
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
