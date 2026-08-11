package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/contracts"
	"github.com/ndzuki/release-manager/internal/store"
)

const (
	maxReleaseNameSearch       = 253
	inventorySyncOperationType = "INVENTORY_SYNC"
)

type inventorySyncCommandPayload struct {
	SyncRequestID string `json:"sync_request_id"`
}

// ListReleases returns one filtered page of cached Helm releases for a cluster.
//
//nolint:gocyclo // inventory filtering validates independent request and tenancy constraints
func (s *Service) ListReleases(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ListReleasesRequest],
) (*connect.Response[orchestratorv1.ListReleasesResponse], error) {
	msg := req.Msg
	if msg.GetCustomerId() == "" {
		return nil, inventoryError(connect.CodeInvalidArgument, "customer_id_required", "customer_id is required")
	}
	if msg.GetClusterId() == "" {
		return nil, inventoryError(connect.CodeInvalidArgument, "cluster_id_required", "cluster_id is required")
	}
	if len(msg.GetNameSearch()) > maxReleaseNameSearch {
		return nil, inventoryError(connect.CodeInvalidArgument, "name_search_too_long", "name_search must not exceed 253 characters")
	}

	pageSize := int(contracts.NormalizePageSize(msg.GetPageSize()))
	status, err := inventoryStatusFromProto(msg.GetStatusFilter())
	if err != nil {
		return nil, err
	}

	customer, err := s.store.Customers().Get(ctx, msg.GetCustomerId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, inventoryError(connect.CodeNotFound, "customer_not_found", "customer not found")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get inventory customer: %w", err))
	}
	cluster, err := s.store.Clusters().Get(ctx, msg.GetClusterId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, inventoryError(connect.CodeNotFound, "cluster_not_found", "cluster not found")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get inventory cluster: %w", err))
	}
	if cluster.CustomerID != customer.ID {
		return nil, inventoryError(connect.CodeNotFound, "cluster_not_found", "cluster not found")
	}

	page, err := s.store.Inventories().Query(ctx, store.InventoryQuery{
		CustomerID: customer.ID,
		ClusterID:  cluster.ID,
		Status:     status,
		NameSearch: strings.TrimSpace(msg.GetNameSearch()),
		PageSize:   pageSize,
		Cursor:     msg.GetCursor(),
	})
	if errors.Is(err, store.ErrInvalidCursor) {
		return nil, inventoryError(connect.CodeInvalidArgument, "invalid_cursor", "inventory cursor is invalid or expired")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("query release inventory: %w", err))
	}

	resp := &orchestratorv1.ListReleasesResponse{
		Releases:   make([]*orchestratorv1.ReleaseSummary, 0, len(page.Items)),
		NextCursor: page.NextCursor,
		TotalCount: int32(page.TotalCount), //nolint:gosec // page totals are bounded by the SQLite row count
	}
	for _, item := range page.Items {
		summary := &orchestratorv1.ReleaseSummary{
			ReleaseDefinitionId: item.ReleaseDefinitionID,
			Namespace:           item.Namespace,
			Name:                item.ReleaseName,
			Chart:               item.Chart,
			ChartVersion:        item.ChartVersion,
			Revision:            int32(item.Revision), //nolint:gosec // Helm revisions are bounded integers
			Status:              inventoryStatusToProto(item.InventoryStatus),
			ValuesDigest:        item.ValuesDigest,
		}
		if !page.LastSyncAt.IsZero() {
			summary.LastSyncAt = timestamppb.New(page.LastSyncAt)
		}
		resp.Releases = append(resp.Releases, summary)
	}
	return connect.NewResponse(resp), nil
}

// TriggerInventorySync persists one manual full-sync command for an online operator.
//
//nolint:gocyclo // sync creation validates independent tenancy, operator, and idempotency constraints
func (s *Service) TriggerInventorySync(
	ctx context.Context,
	req *connect.Request[orchestratorv1.TriggerInventorySyncRequest],
) (*connect.Response[orchestratorv1.TriggerInventorySyncResponse], error) {
	msg := req.Msg
	if msg.GetCustomerId() == "" {
		return nil, inventoryError(connect.CodeInvalidArgument, "customer_id_required", "customer_id is required")
	}
	if msg.GetClusterId() == "" {
		return nil, inventoryError(connect.CodeInvalidArgument, "cluster_id_required", "cluster_id is required")
	}

	customer, err := s.store.Customers().Get(ctx, msg.GetCustomerId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, inventoryError(connect.CodeNotFound, "customer_not_found", "customer not found")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get inventory customer: %w", err))
	}
	cluster, err := s.store.Clusters().Get(ctx, msg.GetClusterId())
	if errors.Is(err, store.ErrNotFound) || (err == nil && cluster.CustomerID != customer.ID) {
		return nil, inventoryError(connect.CodeNotFound, "cluster_not_found", "cluster not found")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get inventory cluster: %w", err))
	}

	operator, err := s.store.Operators().GetByClusterID(ctx, cluster.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, inventoryError(connect.CodeUnavailable, "operator_offline", "operator is offline")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get inventory operator: %w", err))
	}
	if operator.CustomerID != customer.ID {
		return nil, inventoryError(connect.CodeUnavailable, "operator_offline", "operator is offline")
	}
	session, err := s.store.Sessions().GetActiveByOperator(ctx, operator.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && session.Status != store.SessionOnline) {
		return nil, inventoryError(connect.CodeUnavailable, "operator_offline", "operator is offline")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get inventory operator session: %w", err))
	}

	requestID := requestIDOrNew(ctx)
	commandID := uuid.NewString()
	outboxID := uuid.NewString()
	payload, err := json.Marshal(inventorySyncCommandPayload{SyncRequestID: requestID})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode inventory sync command: %w", err))
	}
	syncRequest := &store.InventorySyncRequest{
		ID:         requestID,
		CustomerID: customer.ID,
		ClusterID:  cluster.ID,
		OperatorID: operator.ID,
		CommandID:  commandID,
	}
	created, inserted, err := s.store.InventorySyncRequests().CreateIfAvailable(ctx, syncRequest, &store.OutboxEntry{
		ID:            outboxID,
		CommandID:     commandID,
		OperationID:   requestID,
		OperationType: inventorySyncOperationType,
		OperatorID:    operator.ID,
		Payload:       payload,
		MaxInFlight:   1,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create inventory sync request: %w", err))
	}
	if !inserted {
		return nil, inventorySyncInProgressError(created.ID)
	}

	return connect.NewResponse(&orchestratorv1.TriggerInventorySyncResponse{SyncRequestId: created.ID}), nil
}

func inventorySyncInProgressError(requestID string) *connect.Error {
	err := inventoryError(connect.CodeAlreadyExists, "sync_in_progress", "inventory sync is already in progress")
	err.Meta().Set("X-Sync-Request-ID", requestID)
	return err
}

func inventoryStatusFromProto(status orchestratorv1.ReleaseInventoryStatus) (store.InventoryStatus, error) {
	switch status {
	case orchestratorv1.ReleaseInventoryStatus_RELEASE_INVENTORY_STATUS_UNSPECIFIED:
		return "", nil
	case orchestratorv1.ReleaseInventoryStatus_RELEASE_INVENTORY_STATUS_ACTIVE:
		return store.InventoryActive, nil
	case orchestratorv1.ReleaseInventoryStatus_RELEASE_INVENTORY_STATUS_MISSING:
		return store.InventoryMissing, nil
	case orchestratorv1.ReleaseInventoryStatus_RELEASE_INVENTORY_STATUS_OUT_OF_SYNC:
		return store.InventoryOutOfSync, nil
	default:
		return "", inventoryError(connect.CodeInvalidArgument, "invalid_status_filter", "status_filter is invalid")
	}
}

func inventoryStatusToProto(status store.InventoryStatus) orchestratorv1.ReleaseInventoryStatus {
	switch status {
	case store.InventoryActive:
		return orchestratorv1.ReleaseInventoryStatus_RELEASE_INVENTORY_STATUS_ACTIVE
	case store.InventoryMissing:
		return orchestratorv1.ReleaseInventoryStatus_RELEASE_INVENTORY_STATUS_MISSING
	case store.InventoryOutOfSync:
		return orchestratorv1.ReleaseInventoryStatus_RELEASE_INVENTORY_STATUS_OUT_OF_SYNC
	default:
		return orchestratorv1.ReleaseInventoryStatus_RELEASE_INVENTORY_STATUS_UNSPECIFIED
	}
}

func inventoryError(code connect.Code, reason, message string) *connect.Error {
	err := connect.NewError(code, errors.New(message))
	err.Meta().Set("X-Reason-Code", reason)
	return err
}
