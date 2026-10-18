package sqlite_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// TASK-371: the definition list used to be unscoped whenever customer_id was
// empty. ListForOrganization runs store.DefinitionVisibleToOrganizationPredicate
// (the same text PostgreSQL runs) so each engine is pinned separately: SQLite
// here, the PostgreSQL mirror in store_test.go (integration tag).
func TestDefinitionListForOrganizationScopesToActiveBindings(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	for _, org := range []string{"org-scope-a", "org-scope-b", "org-scope-c", "org-scope-revoked"} {
		require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: org, Name: org}))
	}
	for _, customer := range []string{"cust-scope-a", "cust-scope-b", "cust-scope-c"} {
		require.NoError(t, createCustomerViaManagement(ctx, st, &store.Customer{
			ID: customer, Name: customer, Slug: customer, Status: store.CustomerActive,
		}))
	}
	for _, binding := range []*store.OrgCustomerBinding{
		{ID: uuid.NewString(), OrgID: "org-scope-a", CustomerID: "cust-scope-a"},
		{ID: uuid.NewString(), OrgID: "org-scope-b", CustomerID: "cust-scope-b"},
		{ID: uuid.NewString(), OrgID: "org-scope-c", CustomerID: "cust-scope-c"},
		{ID: uuid.NewString(), OrgID: "org-scope-revoked", CustomerID: "cust-scope-a", Status: store.BindingRevoked},
	} {
		require.NoError(t, st.Bindings().Create(ctx, binding))
	}

	ownerA := "org-scope-a"
	definitions := []*store.ReleaseDefinition{
		{
			ID: "def-a", Name: "def-a", CustomerID: "cust-scope-a", ClusterID: "cls-scope-a",
			Namespace: "ns", ReleaseName: "release-a", Status: store.DefStatusActive,
			OwnerOrganizationID: &ownerA,
		},
		{
			ID: "def-a-disabled", Name: "def-a-disabled", CustomerID: "cust-scope-a", ClusterID: "cls-scope-a",
			Namespace: "ns", ReleaseName: "release-a-disabled", Status: store.DefStatusDisabled,
			OwnerOrganizationID: &ownerA,
		},
		{
			// Owned by org-scope-a but in a customer only org-scope-c is bound to:
			// this is the owner-branch case the predicate deliberately does NOT grant.
			ID: "def-owned-only", Name: "def-owned-only", CustomerID: "cust-scope-c", ClusterID: "cls-scope-c",
			Namespace: "ns", ReleaseName: "release-owned-only", Status: store.DefStatusActive,
			OwnerOrganizationID: &ownerA,
		},
		{
			ID: "def-b", Name: "def-b", CustomerID: "cust-scope-b", ClusterID: "cls-scope-b",
			Namespace: "ns", ReleaseName: "release-b", Status: store.DefStatusActive,
		},
	}
	for _, def := range definitions {
		require.NoError(t, st.Definitions().Create(ctx, def, nil))
	}
	ids := func(defs []*store.ReleaseDefinition) []string {
		out := make([]string, 0, len(defs))
		for _, def := range defs {
			out = append(out, def.ID)
		}
		return out
	}

	t.Run("an empty customer filter returns only the bound tenant", func(t *testing.T) {
		defs, err := st.Definitions().ListForOrganization(ctx, "org-scope-a", "", "", false)
		require.NoError(t, err)
		assert.Equal(t, []string{"def-a"}, ids(defs))
	})

	t.Run("include_disabled keeps the same tenant scope", func(t *testing.T) {
		defs, err := st.Definitions().ListForOrganization(ctx, "org-scope-a", "", "", true)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"def-a", "def-a-disabled"}, ids(defs))
	})

	t.Run("another organization sees its own definitions only", func(t *testing.T) {
		defs, err := st.Definitions().ListForOrganization(ctx, "org-scope-b", "", "", true)
		require.NoError(t, err)
		assert.Equal(t, []string{"def-b"}, ids(defs))
	})

	t.Run("a binding grants the customer even when another organization owns the definition", func(t *testing.T) {
		defs, err := st.Definitions().ListForOrganization(ctx, "org-scope-c", "", "", true)
		require.NoError(t, err)
		assert.Equal(t, []string{"def-owned-only"}, ids(defs))
	})

	t.Run("an explicit foreign customer filter returns nothing", func(t *testing.T) {
		defs, err := st.Definitions().ListForOrganization(ctx, "org-scope-a", "cust-scope-b", "", true)
		require.NoError(t, err)
		assert.Empty(t, defs)
	})

	t.Run("a revoked binding does not grant the list", func(t *testing.T) {
		defs, err := st.Definitions().ListForOrganization(ctx, "org-scope-revoked", "", "", true)
		require.NoError(t, err)
		assert.Empty(t, defs)
	})

	// Pins the semantics chosen from Service.authorizeReadDefinition: the read
	// gate requires an ACTIVE binding and does not treat ownership as a grant.
	// If this assertion ever fails, the change widened who can read a definition
	// and needs an explicit decision, not a silent test update.
	t.Run("ownership without a binding is not a read grant", func(t *testing.T) {
		defs, err := st.Definitions().ListForOrganization(ctx, "org-scope-a", "", "", true)
		require.NoError(t, err)
		assert.NotContains(t, ids(defs), "def-owned-only")
	})

	t.Run("customer and cluster filters still apply", func(t *testing.T) {
		defs, err := st.Definitions().ListForOrganization(ctx, "org-scope-a", "cust-scope-a", "cls-scope-a", false)
		require.NoError(t, err)
		assert.Equal(t, []string{"def-a"}, ids(defs))
	})

	t.Run("an empty organization is refused", func(t *testing.T) {
		_, err := st.Definitions().ListForOrganization(ctx, "", "", "", true)
		require.Error(t, err)
	})
}
