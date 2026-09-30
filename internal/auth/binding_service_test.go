package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func setupBindingService(t *testing.T) (*BindingService, *sqlitestore.Store) {
	t.Helper()

	st, err := sqlitestore.Open("file:" + uuid.New().String() + "?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewBindingService(st, NewStoreCustomerResolver(st), logger), st
}

func bindingActorContext(userID string) context.Context {
	return context.WithValue(context.Background(), userIDKey, userID)
}

func seedBindingActor(t *testing.T, st store.Store, orgID, userID string, role store.Role) {
	t.Helper()
	require.NoError(t, st.Users().Create(context.Background(), &store.User{
		ID:           userID,
		Username:     userID,
		PasswordHash: "not-used",
	}))
	require.NoError(t, st.OrgMembers().Create(context.Background(), &store.OrganizationMember{
		OrgID:  orgID,
		UserID: userID,
		Role:   role,
	}))
}

func seedBindingOrganization(t *testing.T, st store.Store, orgID string) {
	t.Helper()
	require.NoError(t, st.Organizations().Create(context.Background(), &store.Organization{
		ID:   orgID,
		Name: orgID,
	}))
}

func seedBindingCustomer(t *testing.T, st store.Store, customerID string) {
	t.Helper()
	require.NoError(t, createCustomerViaManagement(context.Background(), st, &store.Customer{
		ID:   customerID,
		Name: customerID,
		Slug: customerID,
	}))
}

func TestBindingService_CreateRevokeAndReactivate(t *testing.T) {
	svc, st := setupBindingService(t)
	const (
		orgID      = "org-1"
		customerID = "customer-1"
		userID     = "admin-1"
	)
	seedBindingOrganization(t, st, orgID)
	seedBindingCustomer(t, st, customerID)
	seedBindingActor(t, st, orgID, userID, store.RoleReleaseAdmin)
	ctx := bindingActorContext(userID)

	created, err := svc.CreateBinding(ctx, connect.NewRequest(&authv1.CreateBindingRequest{
		OrgId: orgID, CustomerId: customerID,
	}))
	require.NoError(t, err)
	assert.Equal(t, store.BindingActive, store.BindingStatus(created.Msg.GetBinding().GetStatus()))
	assert.EqualValues(t, 0, created.Msg.GetBinding().GetOptimisticVersion())

	_, err = svc.CreateBinding(ctx, connect.NewRequest(&authv1.CreateBindingRequest{
		OrgId: orgID, CustomerId: customerID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	assert.ErrorContains(t, err, "duplicate_binding")

	revoked, err := svc.RevokeBinding(ctx, connect.NewRequest(&authv1.RevokeBindingRequest{
		BindingId: created.Msg.GetBinding().GetId(), ExpectedVersion: 0,
	}))
	require.NoError(t, err)
	assert.Equal(t, string(store.BindingRevoked), revoked.Msg.GetBinding().GetStatus())
	assert.EqualValues(t, 1, revoked.Msg.GetBinding().GetOptimisticVersion())

	reactivated, err := svc.CreateBinding(ctx, connect.NewRequest(&authv1.CreateBindingRequest{
		OrgId: orgID, CustomerId: customerID,
	}))
	require.NoError(t, err)
	assert.Equal(t, created.Msg.GetBinding().GetId(), reactivated.Msg.GetBinding().GetId())
	assert.Equal(t, string(store.BindingActive), reactivated.Msg.GetBinding().GetStatus())
	assert.EqualValues(t, 2, reactivated.Msg.GetBinding().GetOptimisticVersion())

	var eventCount int
	err = st.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM organization_customer_binding_events WHERE binding_id = ?",
		created.Msg.GetBinding().GetId(),
	).Scan(&eventCount)
	require.NoError(t, err)
	assert.Equal(t, 3, eventCount)
}

func TestBindingService_DisabledCustomerVisibleButNotWritable(t *testing.T) {
	svc, st := setupBindingService(t)
	const (
		orgID      = "org-disabled-customer"
		customerID = "customer-disabled"
		userID     = "admin-disabled"
	)
	seedBindingOrganization(t, st, orgID)
	seedBindingCustomer(t, st, customerID)
	seedBindingActor(t, st, orgID, userID, store.RolePlatformAdmin)
	ctx := bindingActorContext(userID)

	created, err := svc.CreateBinding(ctx, connect.NewRequest(&authv1.CreateBindingRequest{
		OrgId: orgID, CustomerId: customerID,
	}))
	require.NoError(t, err)

	customer, err := st.Customers().Get(ctx, customerID)
	require.NoError(t, err)
	customer.Status = store.CustomerDisabled
	require.NoError(t, st.Customers().Update(ctx, customer, customer.Version))

	got, err := svc.GetBinding(ctx, connect.NewRequest(&authv1.GetBindingRequest{
		BindingId: created.Msg.GetBinding().GetId(),
	}))
	require.NoError(t, err)
	assert.Equal(t, created.Msg.GetBinding().GetId(), got.Msg.GetBinding().GetId())

	listed, err := svc.ListBindings(ctx, connect.NewRequest(&authv1.ListBindingsRequest{OrgId: orgID}))
	require.NoError(t, err)
	assert.Len(t, listed.Msg.GetBindings(), 1)

	_, err = svc.RevokeBinding(ctx, connect.NewRequest(&authv1.RevokeBindingRequest{
		BindingId: created.Msg.GetBinding().GetId(), ExpectedVersion: 0,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.ErrorContains(t, err, "customer_disabled")

	_, err = svc.CreateBinding(ctx, connect.NewRequest(&authv1.CreateBindingRequest{
		OrgId: orgID, CustomerId: customerID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.ErrorContains(t, err, "customer_disabled")
}

func TestBindingService_CrossOrganizationActorDenied(t *testing.T) {
	svc, st := setupBindingService(t)
	const (
		ownOrgID    = "org-own"
		targetOrgID = "org-target"
		customerID  = "customer-cross-org"
		userID      = "admin-own"
	)
	seedBindingOrganization(t, st, ownOrgID)
	seedBindingOrganization(t, st, targetOrgID)
	seedBindingActor(t, st, ownOrgID, userID, store.RolePlatformAdmin)
	ctx := bindingActorContext(userID)
	binding := &store.OrgCustomerBinding{ID: "binding-cross-org", OrgID: targetOrgID, CustomerID: customerID}
	require.NoError(t, st.Bindings().Create(ctx, binding))

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "create",
			call: func() error {
				_, err := svc.CreateBinding(ctx, connect.NewRequest(&authv1.CreateBindingRequest{
					OrgId: targetOrgID, CustomerId: customerID,
				}))
				return err
			},
		},
		{
			name: "get",
			call: func() error {
				_, err := svc.GetBinding(ctx, connect.NewRequest(&authv1.GetBindingRequest{BindingId: binding.ID}))
				return err
			},
		},
		{
			name: "list",
			call: func() error {
				_, err := svc.ListBindings(ctx, connect.NewRequest(&authv1.ListBindingsRequest{OrgId: targetOrgID}))
				return err
			},
		},
		{
			name: "revoke",
			call: func() error {
				_, err := svc.RevokeBinding(ctx, connect.NewRequest(&authv1.RevokeBindingRequest{
					BindingId: binding.ID, ExpectedVersion: 0,
				}))
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			require.Error(t, err)
			assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
			assert.ErrorContains(t, err, "permission_denied")
		})
	}
}

func TestBindingService_RevokeRejectsStaleVersion(t *testing.T) {
	svc, st := setupBindingService(t)
	const (
		orgID      = "org-version"
		customerID = "customer-version"
		userID     = "admin-version"
	)
	seedBindingOrganization(t, st, orgID)
	seedBindingCustomer(t, st, customerID)
	seedBindingActor(t, st, orgID, userID, store.RoleReleaseAdmin)
	ctx := bindingActorContext(userID)
	created, err := svc.CreateBinding(ctx, connect.NewRequest(&authv1.CreateBindingRequest{
		OrgId: orgID, CustomerId: customerID,
	}))
	require.NoError(t, err)

	_, err = svc.RevokeBinding(ctx, connect.NewRequest(&authv1.RevokeBindingRequest{
		BindingId: created.Msg.GetBinding().GetId(), ExpectedVersion: 9,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	assert.ErrorContains(t, err, "optimistic_lock_conflict")
}

type failingCustomerResolver struct{ err error }

func (r failingCustomerResolver) Resolve(context.Context, string) (*store.Customer, error) {
	return nil, r.err
}

// A resolver outage is NOT "customer not found": the two answer different codes, and
// collapsing them would tell an operator to fix the wrong thing.
func TestBindingService_ResolverOutageIsUnavailableNotNotFound(t *testing.T) {
	st, err := sqlitestore.Open("file:" + uuid.New().String() + "?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	const (
		orgID      = "org-outage"
		customerID = "customer-outage"
		userID     = "admin-outage"
	)
	seedBindingOrganization(t, st, orgID)
	seedBindingActor(t, st, orgID, userID, store.RolePlatformAdmin)

	svc := NewBindingService(st, failingCustomerResolver{err: errors.New("store down")}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, err = svc.CreateBinding(bindingActorContext(userID), connect.NewRequest(&authv1.CreateBindingRequest{
		OrgId: orgID, CustomerId: customerID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

// createCustomerViaManagement creates a customer through the canonical atomic seam
// (customer + its active organization binding commit together); the standalone
// Customers().Create had no shipping caller (TASK-226). The synthetic organization is
// derived from the customer id, so fixtures that manage their own organizations and
// bindings are not perturbed.
func createCustomerViaManagement(ctx context.Context, st interface {
	Organizations() store.OrganizationStore
	CustomerCreates() store.CustomerBindingCreateStore
}, customer *store.Customer) error {
	orgID := "org-managed:" + customer.ID
	if err := st.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: orgID}); err != nil {
		// A synthetic organization created by an earlier fixture of the same test is fine.
		if existing, getErr := st.Organizations().Get(ctx, orgID); getErr != nil || existing == nil {
			return err
		}
	}
	return st.CustomerCreates().CreateCustomerWithOrgBinding(ctx, store.CustomerBindingCreateCommand{
		Customer:  customer,
		OrgID:     orgID,
		BindingID: "binding-managed:" + customer.ID,
	})
}
