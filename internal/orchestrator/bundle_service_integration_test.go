//go:build integration

package orchestrator

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
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
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/postgres"
	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
)

// bundleServiceStore opens an isolated PostgreSQL schema and returns a store
// built on it. The bundle ingestion path only exists on PostgreSQL: the SQLite
// engine deliberately fails SubmitBundle, RecordArtifactEvent and ListBundles.
func bundleServiceStore(t *testing.T) store.Store {
	t.Helper()
	baseDSN := os.Getenv("POSTGRES_TEST_DSN")
	if baseDSN == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := t.Context()

	schema := "task143_bundle_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	admin, err := sql.Open("pgx", baseDSN)
	require.NoError(t, err)
	_, err = admin.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA %q`, schema)) //nolint:gosec // schema is generated from a UUID.
	require.NoError(t, err)
	cleanupCtx := context.WithoutCancel(ctx)
	t.Cleanup(func() {
		_, dropErr := admin.ExecContext(cleanupCtx, fmt.Sprintf(`DROP SCHEMA %q CASCADE`, schema)) //nolint:gosec // schema is generated from a UUID.
		require.NoError(t, dropErr)
		require.NoError(t, admin.Close())
	})
	parsed, err := url.Parse(baseDSN)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()

	database, err := postgres.Open(ctx, config.DatabaseConfig{DSN: parsed.String()})
	require.NoError(t, err)
	migrationFS, err := postgres.LoadMigrationFS("../../migrations")
	require.NoError(t, err)
	require.NoError(t, postgres.RunMigrations(ctx, database.SQLDB(), migrationFS))
	st, err := postgresstore.New(database.SQLDB(), database.GORM())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return st
}

// internalBundleContext marks the caller as a service, which skips the
// per-definition authorization ListBundles and GetBundle otherwise require.
func internalBundleContext() context.Context {
	return authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "svc", OrganizationID: "org-1", Service: "orchestrator",
	})
}

// bundleCallerContext is an ordinary user, optionally holding a platform role.
func bundleCallerContext(roles ...string) context.Context {
	return authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-1", OrganizationID: "org-1", Roles: roles,
	})
}

// seedBundle writes one bundle directly, bypassing the ingestion path.
func seedQueryableBundle(t *testing.T, st store.Store, status store.BundleStatus, chartName string) *store.ReleaseBundle {
	t.Helper()
	bundle := &store.ReleaseBundle{
		ID: uuid.NewString(), Name: chartName + "-" + uuid.NewString()[:8], DigestAlg: "sha256",
		DigestValue: uuid.NewString(), Status: status, ChartRef: "oci://reg/charts/" + chartName,
		ChartVersion: "1.0.0", ChartDigest: "sha256:" + strings.Repeat("a", 64),
		SignatureRef: "reg/sig", SBOMRef: "reg/sbom", CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, st.Bundles().Create(t.Context(), bundle))
	return bundle
}

// AC-011-07: with no status filter the list defaults to the non-archived set,
// and the chart-name filter narrows it.
func TestListBundlesDefaultsToTheSelectableStatuses(t *testing.T) {
	st := bundleServiceStore(t)
	svc := NewBundleService(st, nil, nil)
	ctx := internalBundleContext()
	chart := "app-" + uuid.NewString()[:8]

	received := seedQueryableBundle(t, st, store.BundleReceived, chart)
	validated := seedQueryableBundle(t, st, store.BundleValidated, chart)
	seedQueryableBundle(t, st, store.BundleArchived, chart)

	resp, err := svc.ListBundles(ctx, connect.NewRequest(&orchestratorv1.ListBundlesRequest{
		ChartNameFilter: chart,
	}))
	require.NoError(t, err)

	ids := map[string]bool{}
	for _, summary := range resp.Msg.GetBundles() {
		ids[summary.GetId()] = true
	}
	assert.True(t, ids[received.ID], "AC-011-07: received bundles are selectable")
	assert.True(t, ids[validated.ID], "AC-011-07: validated bundles are selectable")
	assert.Len(t, ids, 2, "AC-011-07: archived bundles are not in the default set")
}

// AC-011-08: an explicit status filter overrides the default set.
func TestListBundlesHonoursExplicitStatusFilter(t *testing.T) {
	st := bundleServiceStore(t)
	svc := NewBundleService(st, nil, nil)
	ctx := internalBundleContext()
	chart := "app-" + uuid.NewString()[:8]

	seedQueryableBundle(t, st, store.BundleReceived, chart)
	validated := seedQueryableBundle(t, st, store.BundleValidated, chart)

	resp, err := svc.ListBundles(ctx, connect.NewRequest(&orchestratorv1.ListBundlesRequest{
		ChartNameFilter: chart,
		StatusFilter:    []commonv1.BundleStatus{commonv1.BundleStatus_BUNDLE_STATUS_VALIDATED},
	}))
	require.NoError(t, err)

	require.Len(t, resp.Msg.GetBundles(), 1, "AC-011-08: only validated bundles")
	assert.Equal(t, validated.ID, resp.Msg.GetBundles()[0].GetId())
}

// AC-011-09 / AC-011-18: GetBundle returns an archived bundle in full, but the
// evidence refs are projected away for a caller who is not a platform admin.
func TestGetBundleHidesEvidenceRefsFromNonAdmins(t *testing.T) {
	st := bundleServiceStore(t)
	svc := NewBundleService(st, nil, nil)
	archived := seedQueryableBundle(t, st, store.BundleArchived, "app-"+uuid.NewString()[:8])

	// A platform admin sees the evidence refs.
	adminResp, err := svc.GetBundle(internalBundleContext(), connect.NewRequest(&orchestratorv1.GetBundleRequest{BundleId: archived.ID}))
	require.NoError(t, err)
	assert.Equal(t, "reg/sig", adminResp.Msg.GetBundle().GetSignatureRef(), "an internal caller sees evidence refs")
	assert.Equal(t, "reg/sbom", adminResp.Msg.GetBundle().GetSbomRef())

	// The ordinary caller is authorized through its definition, then projected.
	callerCtx := bundleCallerContext(string(store.RoleReleaseAdmin))
	now := time.Now().UTC()
	// The binding and definition carry foreign keys, so the customer and the
	// organization must exist first.
	require.NoError(t, st.Customers().Create(t.Context(), &store.Customer{
		ID: "cust-1", Name: "cust-1", Slug: "cust-1", Status: store.CustomerActive,
	}))
	require.NoError(t, st.Clusters().Create(t.Context(), &store.Cluster{
		ID: "cls-1", CustomerID: "cust-1", Name: "cls-1", Status: store.ClusterActive,
	}))
	require.NoError(t, st.Organizations().Create(t.Context(), &store.Organization{ID: "org-1", Name: "org-1"}))
	require.NoError(t, st.Users().Create(t.Context(), &store.User{ID: "user-1", Username: "user-1", Status: store.UserActive}))
	// TASK-199: the caller reads the bundle THROUGH this definition, so the definition has
	// to own it. Before the reachability check this link did not exist and the test still
	// passed -- that is exactly the hole being closed.
	require.NoError(t, st.Definitions().Create(t.Context(), &store.ReleaseDefinition{
		ID: "def-1", Name: "app", CustomerID: "cust-1", ClusterID: "cls-1", Namespace: "default",
		ReleaseName: "app", ChartName: "app", Status: store.DefStatusActive, CreatedAt: now,
		CurrentBundleID: &archived.ID,
	}, nil))
	require.NoError(t, st.Bindings().Create(t.Context(), &store.OrgCustomerBinding{
		ID: uuid.NewString(), OrgID: "org-1", CustomerID: "cust-1", Status: store.BindingActive,
		CreatedAt: now, UpdatedAt: now,
	}))

	callerResp, err := svc.GetBundle(callerCtx, connect.NewRequest(&orchestratorv1.GetBundleRequest{
		BundleId: archived.ID, ReleaseDefinitionId: "def-1",
	}))
	require.NoError(t, err)
	assert.Equal(t, archived.ID, callerResp.Msg.GetBundle().GetSummary().GetId(),
		"AC-011-09: an archived bundle is still returned in full")
	assert.Empty(t, callerResp.Msg.GetBundle().GetSignatureRef(),
		"AC-011-18: evidence refs are hidden from a non-admin caller")
	assert.Empty(t, callerResp.Msg.GetBundle().GetSbomRef())
}

// ── TASK-199: bundle reachability (the tenant boundary) ──────────────

// bundleTenantFixture seeds one organization with a customer, a cluster, an active
// binding, a user and a release definition reachable through that binding. (The
// owner_organization_id variant is covered by TestReachableFromOrganizationAcceptsAnOwnedDefinition,
// which exercises the predicate directly.)
func bundleTenantFixture(t *testing.T, st store.Store, suffix string) *store.ReleaseDefinition {
	t.Helper()
	ctx := t.Context()
	orgID := "org-" + suffix
	customerID := "cust-" + suffix
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{
		ID: customerID, Name: customerID, Slug: customerID, Status: store.CustomerActive,
	}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{
		ID: "cls-" + suffix, CustomerID: customerID, Name: "cls-" + suffix, Status: store.ClusterActive,
	}))
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: orgID}))
	require.NoError(t, st.Users().Create(ctx, &store.User{ID: "user-" + suffix, Username: "user-" + suffix, Status: store.UserActive}))
	definition := &store.ReleaseDefinition{
		ID: "def-" + suffix, Name: "app", CustomerID: customerID, ClusterID: "cls-" + suffix,
		Namespace: "default", ReleaseName: "app", ChartName: "app", Status: store.DefStatusActive,
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, st.Definitions().Create(ctx, definition, nil))
	require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{
		ID: uuid.NewString(), OrgID: orgID, CustomerID: customerID, Status: store.BindingActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))
	return definition
}

// setCurrentBundle links a bundle to its definition through the real seam: the submission
// UoW calls this for a validated bundle. Definitions().Update deliberately does NOT write
// current_bundle_id, so a fixture that set the field directly would silently prove nothing.
func setCurrentBundle(t *testing.T, st store.Store, definitionID, bundleID string) {
	t.Helper()
	_, err := st.Definitions().SetCurrentBundle(t.Context(), definitionID, bundleID)
	require.NoError(t, err)
}

// The reported vulnerability: any readable definition id plus any bundle id returned a
// foreign tenant's bundle. The definition authorizes the caller; the BUNDLE has to be
// reachable from the caller's organization.
func TestGetBundleRefusesAnotherOrganizationsBundle(t *testing.T) {
	st := bundleServiceStore(t)
	svc := NewBundleService(st, nil, nil)

	foreign := bundleTenantFixture(t, st, "foreign")
	own := bundleTenantFixture(t, st, "own")
	foreignBundle := seedQueryableBundle(t, st, store.BundleValidated, "app")
	setCurrentBundle(t, st, foreign.ID, foreignBundle.ID)

	// The foreign organization reads its own bundle.
	foreignActor := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-foreign", OrganizationID: "org-foreign", Roles: []string{string(store.RoleReleaseAdmin)},
	})
	resp, err := svc.GetBundle(foreignActor, connect.NewRequest(&orchestratorv1.GetBundleRequest{
		BundleId: foreignBundle.ID, ReleaseDefinitionId: foreign.ID,
	}))
	require.NoError(t, err)
	assert.Equal(t, foreignBundle.ID, resp.Msg.GetBundle().GetSummary().GetId())

	// The other organization may read its OWN definition, but that must not unlock the
	// foreign bundle.
	otherActor := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-own", OrganizationID: "org-own", Roles: []string{string(store.RoleReleaseAdmin)},
	})
	_, err = svc.GetBundle(otherActor, connect.NewRequest(&orchestratorv1.GetBundleRequest{
		BundleId: foreignBundle.ID, ReleaseDefinitionId: own.ID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "not_authorized")
}

// The exception decision C asks for: a bundle that no operation has used yet is still
// readable once it is the definition's current bundle (the submission UoW sets that for a
// validated bundle).
func TestGetBundleAllowsTheDefinitionsCurrentBundleWithoutAnOperation(t *testing.T) {
	st := bundleServiceStore(t)
	svc := NewBundleService(st, nil, nil)
	tenant := bundleTenantFixture(t, st, "current")
	fresh := seedQueryableBundle(t, st, store.BundleValidated, "app")
	setCurrentBundle(t, st, tenant.ID, fresh.ID)

	actor := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-current", OrganizationID: "org-current", Roles: []string{string(store.RoleReleaseAdmin)},
	})
	resp, err := svc.GetBundle(actor, connect.NewRequest(&orchestratorv1.GetBundleRequest{
		BundleId: fresh.ID, ReleaseDefinitionId: tenant.ID,
	}))
	require.NoError(t, err)
	assert.Equal(t, fresh.ID, resp.Msg.GetBundle().GetSummary().GetId())
}

// The predicate has two ways to tie a definition to an organization. The service-level
// authorization (authorizeDefinition) already requires an active customer binding, so the
// owner branch is implied in the GetBundle path -- it is a statement about the DATA, and
// this store-level test pins it directly.
func TestReachableFromOrganizationAcceptsAnOwnedDefinition(t *testing.T) {
	st := bundleServiceStore(t)
	ctx := t.Context()
	customerID := "cust-owned"
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{
		ID: customerID, Name: customerID, Slug: customerID, Status: store.CustomerActive,
	}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{
		ID: "cls-owned", CustomerID: customerID, Name: "cls-owned", Status: store.ClusterActive,
	}))
	owner := "org-owned"
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: owner, Name: owner}))
	bundle := seedQueryableBundle(t, st, store.BundleValidated, "app")
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: "def-owned", Name: "app", CustomerID: customerID, ClusterID: "cls-owned", Namespace: "default",
		ReleaseName: "app", ChartName: "app", Status: store.DefStatusActive, CreatedAt: time.Now().UTC(),
		OwnerOrganizationID: &owner, CurrentBundleID: &bundle.ID,
	}, nil))

	owned, err := st.Bundles().ReachableFromOrganization(ctx, bundle.ID, owner)
	require.NoError(t, err)
	assert.True(t, owned, "a definition owned by the organization reaches its current bundle")

	// No ownership and no binding: the same organization may not read it.
	other, err := st.Bundles().ReachableFromOrganization(ctx, bundle.ID, "org-stranger")
	require.NoError(t, err)
	assert.False(t, other, "an unrelated organization must not reach the bundle")

	// An unknown bundle id is never reachable.
	missing, err := st.Bundles().ReachableFromOrganization(ctx, "no-such-bundle", owner)
	require.NoError(t, err)
	assert.False(t, missing)
}

// The platform admin keeps the global view (the decision's exemption).
func TestGetBundlePlatformAdminKeepsTheGlobalView(t *testing.T) {
	st := bundleServiceStore(t)
	svc := NewBundleService(st, nil, nil)
	bundle := seedQueryableBundle(t, st, store.BundleValidated, "app")

	actor := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "admin-1", OrganizationID: "org-nowhere", Roles: []string{string(store.RolePlatformAdmin)},
	})
	resp, err := svc.GetBundle(actor, connect.NewRequest(&orchestratorv1.GetBundleRequest{
		BundleId: bundle.ID, ReleaseDefinitionId: "def-nowhere",
	}))
	// The definition does not exist, so the definition authorization still refuses: the
	// exemption is about the BUNDLE boundary, not about skipping authorization entirely.
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	tenant := bundleTenantFixture(t, st, "adminview")
	adminOnTenant := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "admin-2", OrganizationID: "org-adminview", Roles: []string{string(store.RolePlatformAdmin)},
	})
	// This bundle is not linked to the tenant's definition at all; an admin may still read it.
	unlinked := seedQueryableBundle(t, st, store.BundleValidated, "other")
	adminResp, err := svc.GetBundle(adminOnTenant, connect.NewRequest(&orchestratorv1.GetBundleRequest{
		BundleId: unlinked.ID, ReleaseDefinitionId: tenant.ID,
	}))
	require.NoError(t, err)
	assert.Equal(t, unlinked.ID, adminResp.Msg.GetBundle().GetSummary().GetId())
	_ = resp
}

// The listing used to match on the chart NAME, so a same-named chart leaked across
// tenants and an unrelated bundle that merely looked similar appeared. It is now scoped to
// the relationship: the definition's current bundle, or a bundle an operation used.
func TestListBundlesExcludesBundlesTheDefinitionDoesNotOwn(t *testing.T) {
	st := bundleServiceStore(t)
	svc := NewBundleService(st, nil, nil)
	tenant := bundleTenantFixture(t, st, "list")

	owned := seedQueryableBundle(t, st, store.BundleValidated, "app")
	setCurrentBundle(t, st, tenant.ID, owned.ID)
	// Same chart ref prefix, no relationship to the definition.
	unrelated := seedQueryableBundle(t, st, store.BundleValidated, "app")

	actor := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-list", OrganizationID: "org-list", Roles: []string{string(store.RoleReleaseAdmin)},
	})
	resp, err := svc.ListBundles(actor, connect.NewRequest(&orchestratorv1.ListBundlesRequest{
		ReleaseDefinitionId: tenant.ID,
	}))
	require.NoError(t, err)

	ids := map[string]bool{}
	for _, summary := range resp.Msg.GetBundles() {
		ids[summary.GetId()] = true
	}
	assert.True(t, ids[owned.ID], "the definition's current bundle must be listed")
	assert.False(t, ids[unrelated.ID], "a same-chart bundle with no relationship must not be listed")
}

// A bundle that is NOT the definition's current bundle but that one of its operations
// used is reachable too -- that is the half of decision C an independent review showed had
// no test at all (disabling the `operations` branch left every test green). Both engines
// share store.ReachableBundlePredicate, so this pins the PostgreSQL side of the same rule.
func TestGetBundleAllowsABundleAnOperationUsed(t *testing.T) {
	st := bundleServiceStore(t)
	svc := NewBundleService(st, nil, nil)
	tenant := bundleTenantFixture(t, st, "opused")

	// Deliberately NOT set as the definition's current bundle.
	used := seedQueryableBundle(t, st, store.BundleValidated, "app")
	require.NoError(t, st.Operations().Create(t.Context(), &store.Operation{
		ID: uuid.NewString(), ReleaseDefinitionID: tenant.ID, BundleID: used.ID,
		Status: store.StatusPending, CreatedAt: time.Now().UTC(),
	}))

	actor := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-opused", OrganizationID: "org-opused", Roles: []string{string(store.RoleReleaseAdmin)},
	})
	resp, err := svc.GetBundle(actor, connect.NewRequest(&orchestratorv1.GetBundleRequest{
		BundleId: used.ID, ReleaseDefinitionId: tenant.ID,
	}))
	require.NoError(t, err)
	assert.Equal(t, used.ID, resp.Msg.GetBundle().GetSummary().GetId())

	// And it appears in the definition's page, which uses the same branch.
	listed, err := svc.ListBundles(actor, connect.NewRequest(&orchestratorv1.ListBundlesRequest{
		ReleaseDefinitionId: tenant.ID,
	}))
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, summary := range listed.Msg.GetBundles() {
		ids[summary.GetId()] = true
	}
	assert.True(t, ids[used.ID], "an operation-used bundle must be listed")

	// A different organization still cannot read it.
	other := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-other", OrganizationID: "org-opused-other", Roles: []string{string(store.RoleReleaseAdmin)},
	})
	_, err = svc.GetBundle(other, connect.NewRequest(&orchestratorv1.GetBundleRequest{
		BundleId: used.ID, ReleaseDefinitionId: tenant.ID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// TASK-223: bundle_aliases has no writer anywhere -- no producer, no backfill migration, and
// the RPC documents only the canonical identifier (unknown id => NOT_FOUND). GetBundle used
// to fall back to GetByAlias, which could therefore never resolve anything while making an
// unknown id look like a supported-but-missing alias. Pin the honest behaviour: even a row
// an operator inserted by hand does not make an unknown id resolve.
func TestGetBundleDoesNotResolveAliases(t *testing.T) {
	st := bundleServiceStore(t)
	svc := NewBundleService(st, nil, nil)

	pgStore, ok := st.(*postgresstore.Store)
	require.True(t, ok, "the alias table only exists on PostgreSQL")

	bundle := seedQueryableBundle(t, st, store.BundleValidated, "app")
	def := bundleTenantFixture(t, st, "alias")
	require.NoError(t, pgStore.GORM().Exec(`
		INSERT INTO bundle_aliases (alias, canonical_bundle_id, alias_type, created_at)
		VALUES (?, ?, 'legacy_id', NOW())
	`, "legacy-bundle-1", bundle.ID).Error)

	actor := authctx.WithActor(context.Background(), authctx.Actor{
		UserID: "user-alias", OrganizationID: "org-alias", Roles: []string{string(store.RoleReleaseAdmin)},
	})
	_, err := svc.GetBundle(actor, connect.NewRequest(&orchestratorv1.GetBundleRequest{
		BundleId: "legacy-bundle-1", ReleaseDefinitionId: def.ID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	// bundleError carries the reason inside the message rather than in X-Reason-Code.
	assert.Contains(t, err.Error(), "bundle_not_found")
}
