// Package orchestrator implements the release orchestration Connect service.
package orchestrator

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)
// maxInventoryPayload is the maximum number of items allowed in a single sync request.
const maxInventoryPayload = 10000

// SyncInventory applies a release inventory snapshot from an operator.
// It implements idempotent upsert (AC-017-01), missing marking (AC-017-02),
// sync_id deduplication (AC-017-03), and guarantees no raw Secret values
// in payload/store/log (AC-017-04).
func (s *Service) SyncInventory(
	ctx context.Context,
	req *connect.Request[orchestratorv1.SyncInventoryRequest],
) (*connect.Response[orchestratorv1.SyncInventoryResponse], error) {
	msg := req.Msg

	// 1. Validate required fields
	if msg.OperatorId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("operator_id is required"))
	}
	if msg.ClusterId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("cluster_id is required"))
	}
	if msg.CustomerId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("customer_id is required"))
	}
	if msg.SyncId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("sync_id is required"))
	}

	// 2. Payload size check
	if len(msg.Items) > maxInventoryPayload {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("payload_too_large: %d items exceeds limit of %d", len(msg.Items), maxInventoryPayload))
	}

	// 3. Idempotency — check if this sync_id has already been applied (AC-017-03)
	existing, err := s.store.Inventories().GetBySyncID(ctx, msg.SyncId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("sync_id lookup: %w", err))
	}
	if existing != nil {
		s.logger.Info("duplicate sync_id, returning cached result",
			"sync_id", msg.SyncId,
			"accepted", existing.AcceptedCount,
			"missing", existing.MissingCount,
		)
		return connect.NewResponse(&orchestratorv1.SyncInventoryResponse{
			AcceptedCount:      int32(existing.AcceptedCount),  //nolint:gosec // inventory count bounded below int32 max
			MissingMarkedCount: int32(existing.MissingCount),   //nolint:gosec // inventory count bounded
			SnapshotVersion:    existing.SnapshotVersion,
			Status:             "duplicate",
		}), nil
	}

	// 4. Collect present keys for MarkMissing
	presentKeys := make([]string, 0, len(msg.Items))
	acceptedCount := 0

	for _, item := range msg.Items {
		// AC-017-04: Log only digest, never values
		s.logger.Debug("upserting inventory item",
			"namespace", item.Namespace,
			"name", item.Name,
			"digest", item.ValuesDigest,
		)

		inventory := &store.ReleaseInventory{
			CustomerID:      msg.CustomerId,
			ClusterID:       msg.ClusterId,
			Namespace:       item.Namespace,
			ReleaseName:     item.Name,
			Chart:           item.Chart,
			ChartVersion:    item.ChartVersion,
			Revision:        int(item.Revision),
			Status:          item.Status,
			ValuesDigest:    item.ValuesDigest,
			InventoryStatus: store.InventoryActive,
			LastSyncID:      msg.SyncId,
			SnapshotVersion: 0, // set below after sync log
		}

		if err := s.store.Inventories().Upsert(ctx, inventory); err != nil {
			return nil, connect.NewError(connect.CodeInternal,
				fmt.Errorf("upsert inventory item %s/%s: %w", item.Namespace, item.Name, err))
		}

		key := item.Namespace + "/" + item.Name
		presentKeys = append(presentKeys, key)
		acceptedCount++
	}

	// 5. Mark missing — releases not in this snapshot get marked InventoryMissing (AC-017-02)
	missingCount := 0
	if msg.FullSnapshot {
		missingCount, err = s.store.Inventories().MarkMissing(ctx, msg.CustomerId, msg.ClusterId, presentKeys)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal,
				fmt.Errorf("mark missing: %w", err))
		}
	}

	// 6. Record sync log for idempotency (AC-017-03)
	// Generate a snapshot version based on sync_id uniqueness
	snapshotVersion := int64(len(msg.SyncId)) // simple versioning scheme

	syncLog := &store.InventorySyncLog{
		SyncID:          msg.SyncId,
		CustomerID:      msg.CustomerId,
		ClusterID:       msg.ClusterId,
		IsFullSnapshot:  msg.FullSnapshot,
		AcceptedCount:   acceptedCount,
		MissingCount:    missingCount,
		SnapshotVersion: snapshotVersion,
	}

	inserted, err := s.store.Inventories().CreateSyncLog(ctx, syncLog)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("create sync log: %w", err))
	}
	if !inserted {
		// This shouldn't happen since we checked up front, but handle it.
		s.logger.Warn("sync_id race, already recorded", "sync_id", msg.SyncId)
	}

	s.logger.Info("inventory sync applied",
		"operator_id", msg.OperatorId,
		"cluster_id", msg.ClusterId,
		"sync_id", msg.SyncId,
		"accepted", acceptedCount,
		"missing", missingCount,
		"full_snapshot", msg.FullSnapshot,
	)

	return connect.NewResponse(&orchestratorv1.SyncInventoryResponse{
		AcceptedCount:      int32(acceptedCount),       //nolint:gosec // inventory count bounded below int32 max
		MissingMarkedCount: int32(missingCount),         //nolint:gosec // inventory count bounded
		SnapshotVersion:    snapshotVersion,
		Status:             "applied",
	}), nil
}

