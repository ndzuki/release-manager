package orchestrator

import (
	"testing"

	"connectrpc.com/connect"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncInventoryFullSnapshotPreservesDefinitionAssociation(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedInventoryScope(t, st, "customer-1", "cluster-1")

	base := func(syncID string, fullSnapshot bool, definitionID string) *connect.Request[orchestratorv1.SyncInventoryRequest] {
		return connect.NewRequest(&orchestratorv1.SyncInventoryRequest{
			OperatorId: "operator-1",
			ClusterId:  "cluster-1",
			CustomerId: "customer-1",
			SyncId:     syncID,
			Items: []*orchestratorv1.InventoryItem{{
				Namespace:    "apps",
				Name:         "example",
				DefinitionId: definitionID,
				Chart:        "example-chart",
				ChartVersion: "1.0.0",
				Revision:     1,
				Status:       "deployed",
			}},
			FullSnapshot: fullSnapshot,
		})
	}

	// Targeted sync establishes the definition association.
	_, err := svc.SyncInventory(t.Context(), base("targeted-sync", false, "definition-1"))
	require.NoError(t, err)

	// The full snapshot sends no DefinitionId and must not erase the
	// association. It changes observable fields (chart/revision/status) so
	// the assertions below prove the second upsert really executed — the
	// test cannot pass merely because the second sync was ignored.
	full := base("full-sync", true, "")
	full.Msg.Items[0].Chart = "example-chart-v2"
	full.Msg.Items[0].ChartVersion = "2.0.0"
	full.Msg.Items[0].Revision = 2
	full.Msg.Items[0].Status = "superseded"
	_, err = svc.SyncInventory(t.Context(), full)
	require.NoError(t, err)

	inventory, err := st.Inventories().GetByDefinition(t.Context(), "definition-1")
	require.NoError(t, err)
	assert.Equal(t, "example", inventory.ReleaseName)
	assert.Equal(t, "example-chart-v2", inventory.Chart)
	assert.Equal(t, "full-sync", inventory.LastSyncID)

	// The cluster still holds exactly one row, and it keeps the definition
	// association while carrying the full-sync fields.
	items, err := st.Inventories().ListByCluster(t.Context(), "customer-1", "cluster-1")
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "definition-1", items[0].ReleaseDefinitionID)
	assert.Equal(t, "full-sync", items[0].LastSyncID)

	// No row may ever appear under an empty definition ID.
	_, err = st.Inventories().GetByDefinition(t.Context(), "")
	assert.ErrorIs(t, err, store.ErrNotFound)
}
