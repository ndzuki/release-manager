package operator

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInventorySyncer_TargetedUpdateIncludesDefinition(t *testing.T) {
	engine := helmengine.NewFake()
	_, err := engine.Install(t.Context(), helmengine.InstallOptions{
		Namespace:   "apps",
		ReleaseName: "example",
		ChartPath:   "example-chart",
	})
	require.NoError(t, err)

	client := new(recordingInventoryClient)
	syncer := NewInventorySyncer(
		engine,
		client,
		"operator-1",
		"customer-1",
		"cluster-1",
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	syncer.doTargetedUpdate(t.Context(), targetedUpdateRequest{
		Namespace:    "apps",
		ReleaseName:  "example",
		OperationID:  "operation-1",
		DefinitionID: "definition-1",
	})

	require.NotNil(t, client.request)
	require.Len(t, client.request.Items, 1)
	assert.Equal(t, "definition-1", client.request.Items[0].GetDefinitionId())
	assert.Equal(t, int32(1), client.request.Items[0].GetRevision())
	assert.False(t, client.request.GetFullSnapshot())
}

type recordingInventoryClient struct {
	request *orchestratorv1.SyncInventoryRequest
}

func (c *recordingInventoryClient) SyncInventory(
	_ context.Context,
	request *connect.Request[orchestratorv1.SyncInventoryRequest],
) (*connect.Response[orchestratorv1.SyncInventoryResponse], error) {
	c.request = request.Msg
	return connect.NewResponse(&orchestratorv1.SyncInventoryResponse{Status: "applied"}), nil
}
