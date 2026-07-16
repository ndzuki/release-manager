// Package operator implements the operator agent inventory syncer (REQ-017).
package operator

import (
	"context"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
)

const (
	defaultSyncInterval        = 5 * time.Minute
	targetedUpdateWindow       = 30 * time.Second
	defaultOrchestratorTimeout = 10 * time.Second
)

// InventorySyncer manages periodic full snapshots and targeted updates
// of Helm release inventory to the orchestrator.
type InventorySyncer struct {
	engine          helmengine.Engine
	orchClient      orchestratorv1connect.OrchestratorServiceClient
	operatorID      string
	customerID      string
	clusterID       string
	syncInterval    time.Duration
	logger          *slog.Logger
	stopCh          chan struct{}
	targetedCh      chan targetedUpdateRequest
}

type targetedUpdateRequest struct {
	Namespace    string
	ReleaseName  string
	OperationID  string
}

// NewInventorySyncer creates a new inventory syncer.
func NewInventorySyncer(
	engine helmengine.Engine,
	orchClient orchestratorv1connect.OrchestratorServiceClient,
	operatorID, customerID, clusterID string,
	logger *slog.Logger,
) *InventorySyncer {
	return &InventorySyncer{
		engine:       engine,
		orchClient:   orchClient,
		operatorID:   operatorID,
		customerID:   customerID,
		clusterID:    clusterID,
		syncInterval: defaultSyncInterval,
		logger:       logger,
		stopCh:       make(chan struct{}),
		targetedCh:   make(chan targetedUpdateRequest, 16),
	}
}

// Start begins the periodic full-snapshot goroutine and the targeted-update listener.
func (s *InventorySyncer) Start(ctx context.Context) {
	go s.fullSyncLoop(ctx)
	go s.targetedUpdateLoop(ctx)
	s.logger.Info("inventory syncer started",
		"operator_id", s.operatorID,
		"interval", s.syncInterval,
	)
}

// Stop signals the syncer to shut down.
func (s *InventorySyncer) Stop() {
	close(s.stopCh)
}

// NotifyOperationComplete queues a targeted inventory update for a release
// that just completed an operation. Must be called within 30s of operation completion.
func (s *InventorySyncer) NotifyOperationComplete(namespace, releaseName, operationID string) {
	select {
	case s.targetedCh <- targetedUpdateRequest{
		Namespace:   namespace,
		ReleaseName: releaseName,
		OperationID: operationID,
	}:
	default:
		s.logger.Warn("targeted update channel full, dropping update",
			"namespace", namespace,
			"release", releaseName,
		)
	}
}

// fullSyncLoop periodically emits full snapshots.
func (s *InventorySyncer) fullSyncLoop(ctx context.Context) {
	ticker := time.NewTicker(s.syncInterval)
	defer ticker.Stop()

	// Initial sync after a short delay to let the operator settle.
	initialDelay := 5 * time.Second
	select {
	case <-time.After(initialDelay):
	case <-s.stopCh:
		return
	case <-ctx.Done():
		return
	}

	// Run the first sync immediately.
	s.doSync(ctx, true)

	for {
		select {
		case <-ticker.C:
			s.doSync(ctx, true)
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// targetedUpdateLoop processes targeted update requests.
func (s *InventorySyncer) targetedUpdateLoop(ctx context.Context) {
	for {
		select {
		case req := <-s.targetedCh:
			s.doTargetedUpdate(ctx, req)
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// doSync sends a full snapshot to the orchestrator.
func (s *InventorySyncer) doSync(ctx context.Context, fullSnapshot bool) {
	syncCtx, cancel := context.WithTimeout(ctx, defaultOrchestratorTimeout)
	defer cancel()

	items, err := s.engine.List(syncCtx, "") // empty namespace = all namespaces
	if err != nil {
		s.logger.Error("failed to list releases for inventory sync", "error", err)
		return
	}

	// Convert to proto items
	pbItems := make([]*orchestratorv1.InventoryItem, 0, len(items))
	for _, item := range items {
		pbItems = append(pbItems, &orchestratorv1.InventoryItem{
			Namespace:    item.Namespace,
			Name:         item.Name,
			Chart:        item.Chart,
			ChartVersion: item.ChartVersion,
			Revision:     int32(item.Revision),  //nolint:gosec // Helm revision bounded
			Status:       item.Status,
			ValuesDigest: item.ValuesDigest,
		})
	}

	syncID := uuid.New().String()

	req := &orchestratorv1.SyncInventoryRequest{
		OperatorId:   s.operatorID,
		ClusterId:    s.clusterID,
		CustomerId:   s.customerID,
		SyncId:       syncID,
		Items:        pbItems,
		FullSnapshot: fullSnapshot,
	}

	resp, err := s.orchClient.SyncInventory(syncCtx, connect.NewRequest(req))
	if err != nil {
		s.logger.Error("inventory sync failed",
			"sync_id", syncID,
			"items", len(pbItems),
			"error", err,
		)
		return
	}

	s.logger.Info("inventory sync completed",
		"sync_id", syncID,
		"accepted", resp.Msg.AcceptedCount,
		"missing", resp.Msg.MissingMarkedCount,
		"status", resp.Msg.Status,
	)
}

// doTargetedUpdate sends a single-release update to the orchestrator.
func (s *InventorySyncer) doTargetedUpdate(ctx context.Context, req targetedUpdateRequest) {
	syncCtx, cancel := context.WithTimeout(ctx, defaultOrchestratorTimeout)
	defer cancel()

	// Get current status of the specific release.
	rel, err := s.engine.Status(syncCtx, helmengine.StatusOptions{
		Namespace:   req.Namespace,
		ReleaseName: req.ReleaseName,
	})
	if err != nil {
		s.logger.Warn("failed to get release status for targeted update",
			"namespace", req.Namespace,
			"release", req.ReleaseName,
			"error", err,
		)
		return
	}

	syncID := uuid.New().String()

	updateReq := &orchestratorv1.SyncInventoryRequest{
		OperatorId:   s.operatorID,
		ClusterId:    s.clusterID,
		CustomerId:   s.customerID,
		SyncId:       syncID,
		FullSnapshot: false,
		Items: []*orchestratorv1.InventoryItem{{
			Namespace:    rel.Namespace,
			Name:         rel.Name,
			Chart:        rel.Chart,
			ChartVersion: rel.Chart,
			Revision:     int32(rel.Revision),  //nolint:gosec // Helm revision bounded
			Status:       rel.Status,
			ValuesDigest: rel.ManifestDigest,
		}},
	}

	resp, err := s.orchClient.SyncInventory(syncCtx, connect.NewRequest(updateReq))
	if err != nil {
		s.logger.Error("targeted inventory update failed",
			"sync_id", syncID,
			"namespace", req.Namespace,
			"release", req.ReleaseName,
			"error", err,
		)
		return
	}

	s.logger.Info("targeted inventory update completed",
		"sync_id", syncID,
		"namespace", req.Namespace,
		"release", req.ReleaseName,
		"status", resp.Msg.Status,
	)
}
