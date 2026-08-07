package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

func TestInventorySyncRequestCreateIfAvailable(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	first := &store.InventorySyncRequest{
		ID: "sync-request-1", CustomerID: "customer-1", ClusterID: "cluster-1",
		OperatorID: "operator-1", CommandID: "command-1",
	}
	firstOutbox := &store.OutboxEntry{
		ID: "outbox-1", CommandID: first.CommandID, OperationID: first.ID,
		OperationType: "INVENTORY_SYNC", OperatorID: first.OperatorID, Payload: []byte(`{"sync_request_id":"sync-request-1"}`),
	}
	created, inserted, err := st.InventorySyncRequests().CreateIfAvailable(ctx, first, firstOutbox)
	require.NoError(t, err)
	require.True(t, inserted)
	assert.Equal(t, first.ID, created.ID)
	_, err = st.Outbox().Get(ctx, firstOutbox.ID)
	require.NoError(t, err)

	second := &store.InventorySyncRequest{
		ID: "sync-request-2", CustomerID: "customer-1", ClusterID: "cluster-1",
		OperatorID: "operator-1", CommandID: "command-2",
	}
	existing, inserted, err := st.InventorySyncRequests().CreateIfAvailable(ctx, second, &store.OutboxEntry{
		ID: "outbox-2", CommandID: second.CommandID, OperationID: second.ID,
		OperationType: "INVENTORY_SYNC", OperatorID: second.OperatorID, Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	assert.False(t, inserted)
	assert.Equal(t, first.ID, existing.ID)
	_, err = st.Outbox().Get(ctx, "outbox-2")
	assert.ErrorIs(t, err, store.ErrNotFound)

	require.NoError(t, st.InventorySyncRequests().UpdateStatus(ctx, first.ID, store.InventorySyncSucceeded, ""))
	created, inserted, err = st.InventorySyncRequests().CreateIfAvailable(ctx, second, &store.OutboxEntry{
		ID: "outbox-2", CommandID: second.CommandID, OperationID: second.ID,
		OperationType: "INVENTORY_SYNC", OperatorID: second.OperatorID, Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	assert.True(t, inserted)
	assert.Equal(t, second.ID, created.ID)
}
