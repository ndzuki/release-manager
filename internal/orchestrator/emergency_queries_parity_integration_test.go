//go:build integration

package orchestrator

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/internal/trust"
)

// G11 parity (Lead ruling ②): the emergency candidate list must answer
// identically on both engines, and it must answer "was this artifact
// validated?" — never "can it be pulled right now?".
//
// A validated artifact without a candidate_artifact_locations row is still a
// candidate: a missing location is a pull-time concern for the operator, and
// the central read model must not report it as "does not exist". The store-level
// parity gate lives in
// internal/store/postgres/candidate_artifacts_parity_integration_test.go; this
// test pins the same contract at the RPC layer the frontend actually consumes,
// so neither the PostgreSQL predicate nor the orchestrator projection can drift
// back to a location requirement or drop the validated_at guard.
//
// The PostgreSQL half needs POSTGRES_TEST_DSN (CI job: test-postgres-integration)
// and is skipped without it; the SQLite half runs whenever the integration tag
// is set.
func TestListCandidateArtifactsEngineParity(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		st, err := sqlitestore.Open(t.TempDir() + "/parity.db")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, st.Close()) })

		assertCandidateArtifactEngineParity(t, st)
	})
	t.Run("postgres", func(t *testing.T) {
		// bundleServiceStore opens an isolated PostgreSQL schema with the full
		// migration set (bundle_service_integration_test.go); this test only
		// needs the store, not the bundle fixtures.
		assertCandidateArtifactEngineParity(t, bundleServiceStore(t))
	})
}

// assertCandidateArtifactEngineParity runs one identical fixture through
// ListCandidateArtifacts on the given engine and asserts the G11 contract.
func assertCandidateArtifactEngineParity(t *testing.T, st store.Store) {
	t.Helper()
	svc := parityService(t, st)
	seedParityEmergencyScope(t, st)
	seedParityCandidateArtifacts(t, st)

	resp, err := svc.ListCandidateArtifacts(deployerCtx(), connect.NewRequest(&orchestratorv1.ListCandidateArtifactsRequest{
		OrganizationId: "org-001", ReleaseDefinitionId: "def-001",
	}))
	require.NoError(t, err)

	ids := make(map[string]bool, len(resp.Msg.GetArtifacts()))
	for _, artifact := range resp.Msg.GetArtifacts() {
		ids[artifact.GetId()] = true
	}
	assert.True(t, ids["parity-validated-with-location"], "a validated artifact with a location row is a candidate")
	assert.True(t, ids["parity-validated-no-location"],
		"G11 ②: a validated artifact without a location row is still a candidate")
	assert.False(t, ids["parity-unvalidated"], "an unvalidated artifact is never a candidate")
	assert.Len(t, ids, 2, "ListCandidateArtifacts must not return anything else")
}

// parityService mirrors setupService's wiring on an arbitrary engine.
func parityService(t *testing.T, st store.Store) *Service {
	t.Helper()
	// store.Store does not expose the unit of work; both concrete engines do.
	uowStore, ok := st.(interface {
		OperationCreationUnitOfWork() store.OperationCreationUnitOfWork
	})
	require.True(t, ok, "store must expose the operation creation unit of work")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	verifier := trust.NewStubVerifier(st.Verifications(), nil, logger)
	svc := NewService(st, verifier, "staging", nil, uowStore.OperationCreationUnitOfWork(), authorization.NewStoreAuthorizer(st), logger)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(shutdownCtx))
	})
	return svc
}

// seedParityEmergencyScope seeds the minimum tenancy graph
// authorizeEmergencyRead requires for def-001 (customer, organization, user,
// cluster, definition and an active org-customer binding).
func seedParityEmergencyScope(t *testing.T, st store.Store) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, st.Customers().Create(t.Context(), &store.Customer{
		ID: "cust-001", Name: "cust-001", Slug: "cust-001", Status: store.CustomerActive,
	}))
	require.NoError(t, st.Organizations().Create(t.Context(), &store.Organization{ID: "org-001", Name: "org-001"}))
	require.NoError(t, st.Users().Create(t.Context(), &store.User{
		ID: "user-001", Username: "user-001", Status: store.UserActive,
	}))
	require.NoError(t, st.Clusters().Create(t.Context(), &store.Cluster{
		ID: "cls-001", Name: "cls-001", CustomerID: "cust-001", Status: store.ClusterActive,
	}))
	require.NoError(t, st.Bindings().Create(t.Context(), &store.OrgCustomerBinding{
		ID: "binding-001", OrgID: "org-001", CustomerID: "cust-001", Status: store.BindingActive,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, st.Definitions().Create(t.Context(), &store.ReleaseDefinition{
		ID: "def-001", Name: "my-release", CustomerID: "cust-001", ClusterID: "cls-001",
		Namespace: "default", ReleaseName: "my-release", ChartName: "nginx",
		Status: store.DefStatusActive, CreatedBy: "test", CreatedAt: now, UpdatedAt: now,
	}, nil))
}

// seedParityCandidateArtifacts writes the three candidate artifacts the parity
// assertion needs, through the public store API only.
func seedParityCandidateArtifacts(t *testing.T, st store.Store) {
	t.Helper()
	validatedAt := time.Now().UTC().Add(-time.Minute)
	// Validated, but deliberately without a location row: the PostgreSQL legacy
	// Create only upserts a location for a non-empty ref, so an empty ref
	// reproduces the "validated but not yet located" state that
	// RecordArtifactEvent can produce.
	require.NoError(t, st.CandidateArtifacts().Create(t.Context(), &store.CandidateArtifact{
		ID: "parity-validated-no-location", ArtifactType: store.ArtifactImage,
		Ref: "", Digest: "sha256:parity-no-location", ValidatedAt: &validatedAt, SourceID: "source-parity",
	}))
	// Validated with a location row.
	require.NoError(t, st.CandidateArtifacts().Create(t.Context(), &store.CandidateArtifact{
		ID: "parity-validated-with-location", ArtifactType: store.ArtifactImage,
		Ref: "registry.example/team/api:1.0.0", Digest: "sha256:parity-with-location",
		ValidatedAt: &validatedAt, SourceID: "source-parity",
	}))
	// Never validated: must not be a candidate on either engine.
	require.NoError(t, st.CandidateArtifacts().Create(t.Context(), &store.CandidateArtifact{
		ID: "parity-unvalidated", ArtifactType: store.ArtifactImage,
		Ref: "registry.example/team/api:2.0.0", Digest: "sha256:parity-unvalidated", SourceID: "source-parity",
	}))
}
