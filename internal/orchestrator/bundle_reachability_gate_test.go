package orchestrator

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	authctx "github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/internal/trust"
)

// TASK-369: reachability is owner OR active binding, and revoking the binding does not
// erase the owner relation (the relation is data ownership, see docs/architecture.md
// 1.5.1). The residual risk that raised this card is that the broader relation becomes an
// independent grant. This test pins the actual boundary instead of the predicate text: every
// ENTRY POINT requires the active binding before the predicate is consulted, so a revoked
// binding stops reads and claims even when the definition is owned by the caller's
// organization.
//
// Mutation evidence (recorded on the TASK-369 card): removing the active-binding gate from
// authorizeDefinition turns the read half red; the write half is the same gate through
// AuthorizeWrite. Changing the predicate's "OR" to "AND" turns the store-level pin red
// (internal/store/sqlite/bundles_reachability_test.go, "the owner keeps the relation after
// its own binding is revoked").
func TestBundleTenantBoundaryAfterOwnerBindingRevoke(t *testing.T) {
	ctx := context.Background()
	st := sqlitestore.OpenTest(t)
	now := time.Now().UTC()
	logger := slog.New(slog.DiscardHandler)

	orgID, customerID := "org-369", "cust-369"
	require.NoError(t, createCustomerViaManagement(ctx, st, &store.Customer{
		ID: customerID, Name: customerID, Slug: customerID, Status: store.CustomerActive,
	}))
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: orgID, Status: store.OrgActive}))
	binding := &store.OrgCustomerBinding{
		ID: uuid.NewString(), OrgID: orgID, CustomerID: customerID, Status: store.BindingActive,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, st.Bindings().Create(ctx, binding))
	require.NoError(t, st.Users().Create(ctx, &store.User{ID: "user-369", Username: "user-369", Status: store.UserActive}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: orgID, UserID: "user-369", Role: store.RoleReleaseAdmin,
	}))

	// The definition is OWNED by the organization and holds the bundle; the binding is the
	// only thing that can be withdrawn.
	definition := &store.ReleaseDefinition{
		ID: "def-369", Name: "app", CustomerID: customerID, ClusterID: "cls-369",
		Namespace: "default", ReleaseName: "app", ChartName: "app", Status: store.DefStatusActive,
		OwnerOrganizationID: &orgID, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, st.Definitions().Create(ctx, definition, nil))
	bundle := &store.ReleaseBundle{
		ID: uuid.NewString(), Name: "bundle-369", DigestAlg: "sha256", DigestValue: uuid.NewString(),
		Status: store.BundleValidated, ChartRef: "app", CreatedAt: now,
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))
	require.NoError(t, writeCurrentBundleDirect(ctx, st, definition.ID, bundle.ID))

	actor := authctx.WithActor(ctx, authctx.Actor{UserID: "user-369", OrganizationID: orgID})
	svc := NewService(st, trust.NewStubVerifier(st.Verifications(), nil, logger), "staging", nil,
		st.OperationCreationUnitOfWork(), authorization.NewStoreAuthorizer(st), logger)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(shutdownCtx))
	})
	bundles := NewBundleService(st, logger, nil)

	getBundle := func() error {
		_, err := bundles.GetBundle(actor, connect.NewRequest(&orchestratorv1.GetBundleRequest{
			BundleId: bundle.ID, ReleaseDefinitionId: definition.ID,
		}))
		return err
	}
	createOperation := func(key string) error {
		_, err := svc.CreateOperation(actor, withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
			OperationType: "INSTALL", BundleId: bundle.ID, ReleaseDefinitionId: definition.ID,
		}), key))
		return err
	}

	// Positive control: with the binding active the owner reads the bundle and passes the
	// write path's authorization gate (it may still fail a later gate; it must not be a
	// permission denial).
	require.NoError(t, getBundle(), "an active binding plus owner must be readable")
	require.NotEqual(t, connect.CodePermissionDenied, connect.CodeOf(createOperation("before-revoke")),
		"the write gate must not deny while the binding is active")

	require.NoError(t, st.Bindings().SetStatus(ctx, binding.ID, store.BindingRevoked))

	// The store-level relation survives the revocation (it is ownership, not a grant).
	reachable, err := st.Bundles().ReachableFromOrganization(ctx, bundle.ID, orgID)
	require.NoError(t, err)
	assert.True(t, reachable, "the owner branch is data ownership and does not depend on the binding status")

	// ...and it grants nothing: both entry points still require the active binding.
	err = getBundle()
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"a revoked binding must stop GetBundle even though the definition is owned by the caller")
	// bundleError carries the reason code in the message, not in X-Reason-Code meta.
	assert.Contains(t, err.Error(), "not_authorized")

	err = createOperation("after-revoke")
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"a revoked binding must stop CreateOperation even though the definition is owned by the caller")

	// The revocation, not the ownership, is what denied the calls: restoring the binding
	// restores access.
	require.NoError(t, st.Bindings().SetStatus(ctx, binding.ID, store.BindingActive))
	require.NoError(t, getBundle(), "re-activating the binding must restore the read")
}
