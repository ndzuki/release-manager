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
	require.NoError(t, st.Definitions().Create(t.Context(), &store.ReleaseDefinition{
		ID: "def-1", Name: "app", CustomerID: "cust-1", ClusterID: "cls-1", Namespace: "default",
		ReleaseName: "app", ChartName: "app", Status: store.DefStatusActive, CreatedAt: now,
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
