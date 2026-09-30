package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// TASK-199: the tenant boundary for reading a bundle. SQLite runs the same predicate as
// PostgreSQL (store.ReachableBundlePredicate) and the service tests exercise it through the
// orchestrator, but an independent review found the SQLite branch had no committed test at
// all -- and that disabling the `operations` branch left every test green. Each branch is
// pinned separately here.
type reachabilityFixture struct {
	store      store.Store
	orgID      string
	customerID string
	definition *store.ReleaseDefinition
}

func seedReachabilityFixture(t *testing.T) reachabilityFixture {
	t.Helper()
	st := setupStore(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	orgID := "org-" + suffix
	customerID := "cust-" + suffix

	require.NoError(t, createCustomerViaManagement(ctx, st, &store.Customer{
		ID: customerID, Name: customerID, Slug: customerID, Status: store.CustomerActive,
	}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{
		ID: "cls-" + suffix, CustomerID: customerID, Name: "cls-" + suffix, Status: store.ClusterActive,
	}))
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: orgID}))
	// The definition names the organization as its owner: without this the first predicate
	// branch is false for every subtest and the fixture would prove nothing (it did fail
	// that way before this line was added).
	definition := &store.ReleaseDefinition{
		ID: "def-" + suffix, Name: "app", CustomerID: customerID, ClusterID: "cls-" + suffix,
		Namespace: "default", ReleaseName: "app", ChartName: "app", Status: store.DefStatusActive,
		CreatedAt: time.Now().UTC(), OwnerOrganizationID: &orgID,
	}
	require.NoError(t, st.Definitions().Create(ctx, definition, nil))
	return reachabilityFixture{store: st, orgID: orgID, customerID: customerID, definition: definition}
}

func (f reachabilityFixture) bundle(t *testing.T) *store.ReleaseBundle {
	t.Helper()
	bundle := &store.ReleaseBundle{
		ID: uuid.NewString(), Name: "b-" + uuid.NewString()[:8], DigestAlg: "sha256",
		DigestValue: uuid.NewString(), Status: store.BundleValidated, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, f.store.Bundles().Create(context.Background(), bundle))
	return bundle
}

func (f reachabilityFixture) reachable(t *testing.T, bundleID, orgID string) bool {
	t.Helper()
	ok, err := f.store.Bundles().ReachableFromOrganization(context.Background(), bundleID, orgID)
	require.NoError(t, err)
	return ok
}

func TestReachableFromOrganizationBranches(t *testing.T) {
	t.Run("an organization owns the definition that holds the bundle", func(t *testing.T) {
		f := seedReachabilityFixture(t)
		bundle := f.bundle(t)
		err := setCurrentBundleDirect(context.Background(), f.store, f.definition.ID, bundle.ID)
		require.NoError(t, err)

		assert.True(t, f.reachable(t, bundle.ID, f.orgID), "owner + current bundle")
		assert.False(t, f.reachable(t, bundle.ID, "org-stranger"), "a foreign organization must not reach it")
	})

	t.Run("an active customer binding reaches the definition", func(t *testing.T) {
		f := seedReachabilityFixture(t)
		bundle := f.bundle(t)
		err := setCurrentBundleDirect(context.Background(), f.store, f.definition.ID, bundle.ID)
		require.NoError(t, err)
		require.NoError(t, f.store.Organizations().Create(context.Background(), &store.Organization{ID: "org-bound", Name: "org-bound"}))
		require.NoError(t, f.store.Bindings().Create(context.Background(), &store.OrgCustomerBinding{
			ID: uuid.NewString(), OrgID: "org-bound", CustomerID: f.customerID, Status: store.BindingActive,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}))

		assert.True(t, f.reachable(t, bundle.ID, "org-bound"), "the active binding carries the reachability")
	})

	t.Run("a revoked binding does not", func(t *testing.T) {
		f := seedReachabilityFixture(t)
		bundle := f.bundle(t)
		err := setCurrentBundleDirect(context.Background(), f.store, f.definition.ID, bundle.ID)
		require.NoError(t, err)
		require.NoError(t, f.store.Organizations().Create(context.Background(), &store.Organization{ID: "org-revoked", Name: "org-revoked"}))
		require.NoError(t, f.store.Bindings().Create(context.Background(), &store.OrgCustomerBinding{
			ID: uuid.NewString(), OrgID: "org-revoked", CustomerID: f.customerID, Status: store.BindingRevoked,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}))

		assert.False(t, f.reachable(t, bundle.ID, "org-revoked"), "a revoked binding is not a relationship")
	})

	// This is the branch a review showed had no test: a bundle no definition points at is
	// still reachable when an operation of one of the organization's definitions used it.
	t.Run("an operation of the definition used the bundle", func(t *testing.T) {
		f := seedReachabilityFixture(t)
		bundle := f.bundle(t)
		require.NoError(t, f.store.Operations().Create(context.Background(), &store.Operation{
			ID: uuid.NewString(), ReleaseDefinitionID: f.definition.ID, BundleID: bundle.ID,
			Status: store.StatusPending, CreatedAt: time.Now().UTC(),
		}))

		assert.True(t, f.reachable(t, bundle.ID, f.orgID), "operation-used bundle is reachable")
		assert.False(t, f.reachable(t, bundle.ID, "org-stranger"), "only for the organization that used it")
	})

	t.Run("unknown and empty ids are never reachable", func(t *testing.T) {
		f := seedReachabilityFixture(t)
		bundle := f.bundle(t)

		assert.False(t, f.reachable(t, "no-such-bundle", f.orgID))
		assert.False(t, f.reachable(t, bundle.ID, ""))
		assert.False(t, f.reachable(t, "", f.orgID))
	})
}

// TASK-215: the write path's boundary. A bundle claimed by ANOTHER organization may not be
// taken; an unclaimed one may (that is how a first install works, since the definition only
// becomes the owner when the operation is created).
func TestBundleClaimedOutsideOrganization(t *testing.T) {
	f := seedReachabilityFixture(t)
	ctx := context.Background()

	// Empty inputs answer "not claimed", which is fail-open by construction: the caller
	// (CreateOperation) has already authenticated an actor with a non-empty organization
	// and resolved the bundle, so this guard only keeps the SQL from running nonsense.
	for name, args := range map[string][2]string{
		"empty bundle": {"", "org-stranger"},
		"empty org":    {"bundle-any", ""},
	} {
		claimed, guardErr := f.store.Bundles().BundleClaimedOutsideOrganization(ctx, args[0], args[1])
		require.NoError(t, guardErr, name)
		assert.False(t, claimed, "%s: the guard must not claim ownership", name)
	}

	unclaimed := f.bundle(t)
	claimed, err := f.store.Bundles().BundleClaimedOutsideOrganization(ctx, unclaimed.ID, "org-stranger")
	require.NoError(t, err)
	assert.False(t, claimed, "an unclaimed bundle is not claimed by another organization")

	// The fixture's definition (owned by f.orgID) adopts the bundle.
	err = setCurrentBundleDirect(ctx, f.store, f.definition.ID, unclaimed.ID)
	require.NoError(t, err)

	for name, org := range map[string]string{"owner": f.orgID, "stranger": "org-stranger"} {
		claimed, err := f.store.Bundles().BundleClaimedOutsideOrganization(ctx, unclaimed.ID, org)
		require.NoError(t, err)
		if name == "owner" {
			assert.False(t, claimed, "the owning organization does not see a foreign claim")
		} else {
			assert.True(t, claimed, "another organization must see the claim")
		}
	}
}

// setCurrentBundleDirect writes the definition pointer the operation-creation UoW writes:
// DefinitionStore.SetCurrentBundle was removed with the TASK-226 dead-surface batch (no
// shipping caller), so fixtures that need the pointer write it themselves.
func setCurrentBundleDirect(ctx context.Context, st store.Store, definitionID, bundleID string) error {
	sqliteStore, ok := st.(interface{ DB() *sql.DB })
	if !ok {
		return fmt.Errorf("store has no DB accessor")
	}
	_, err := sqliteStore.DB().ExecContext(ctx,
		`UPDATE release_definitions SET current_bundle_id = ? WHERE id = ?`, bundleID, definitionID)
	return err
}
