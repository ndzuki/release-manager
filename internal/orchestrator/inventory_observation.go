package orchestrator

import (
	"context"
	"errors"
	"sort"

	"connectrpc.com/connect"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

// ListReleaseInventory returns the active organization's complete observed
// inventory view. The store query is unfiltered so E2E cleanup can discover
// every release, while organization bindings preserve tenant isolation.
// Internal digests, sync metadata, workload identity, and capabilities are
// deliberately not represented by the response contract (REQ-066).
func (s *Service) ListReleaseInventory(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ListReleaseInventoryRequest],
) (*connect.Response[orchestratorv1.ListReleaseInventoryResponse], error) {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok || actor.OrganizationID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("actor organization is required"))
	}

	bindings, err := s.store.Bindings().ListByOrg(ctx, actor.OrganizationID)
	if err != nil {
		return nil, s.inventoryObservationInternal("list inventory bindings", err)
	}
	visibleCustomers := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if binding != nil && binding.Status == store.BindingActive {
			visibleCustomers[binding.CustomerID] = struct{}{}
		}
	}

	items, err := s.store.Inventories().ListAll(ctx)
	if err != nil {
		return nil, s.inventoryObservationInternal("list release inventory", err)
	}

	customers := make(map[string]string)
	clusters := make(map[string]string)
	definitions := make(map[string]string)
	rows := make([]*orchestratorv1.ReleaseInventoryRow, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		if _, visible := visibleCustomers[item.CustomerID]; !visible {
			continue
		}

		row := &orchestratorv1.ReleaseInventoryRow{
			CustomerId:          item.CustomerID,
			ClusterId:           item.ClusterID,
			ReleaseDefinitionId: item.ReleaseDefinitionID,
			Namespace:           item.Namespace,
			ReleaseName:         item.ReleaseName,
			Revision:            int32(item.Revision), //nolint:gosec // release revisions are bounded positive integers
			Status:              inventoryStatusToProto(item.InventoryStatus),
		}

		if _, cached := customers[item.CustomerID]; !cached && item.CustomerID != "" {
			customer, lookupErr := s.store.Customers().Get(ctx, item.CustomerID)
			if lookupErr != nil && !errors.Is(lookupErr, store.ErrNotFound) {
				return nil, s.inventoryObservationInternal("get inventory customer", lookupErr)
			}
			if customer != nil {
				customers[item.CustomerID] = customer.Name
			}
		}
		row.CustomerName = customers[item.CustomerID]

		if _, cached := clusters[item.ClusterID]; !cached && item.ClusterID != "" {
			cluster, lookupErr := s.store.Clusters().Get(ctx, item.ClusterID)
			if lookupErr != nil && !errors.Is(lookupErr, store.ErrNotFound) {
				return nil, s.inventoryObservationInternal("get inventory cluster", lookupErr)
			}
			if cluster != nil {
				clusters[item.ClusterID] = cluster.Name
			}
		}
		row.ClusterName = clusters[item.ClusterID]

		if item.ReleaseDefinitionID != "" {
			if _, cached := definitions[item.ReleaseDefinitionID]; !cached {
				definition, lookupErr := s.store.Definitions().Get(ctx, item.ReleaseDefinitionID)
				if lookupErr != nil && !errors.Is(lookupErr, store.ErrNotFound) {
					return nil, s.inventoryObservationInternal("get inventory release definition", lookupErr)
				}
				if definition != nil {
					definitions[item.ReleaseDefinitionID] = definition.Name
				}
			}
			row.ReleaseDefinitionName = definitions[item.ReleaseDefinitionID]

			active, lookupErr := s.store.Operations().GetActiveForDefinition(ctx, item.ReleaseDefinitionID)
			switch {
			case lookupErr == nil && active != nil && !active.Status.IsTerminal():
				row.ActiveOperation = &orchestratorv1.ActiveOperationRef{
					OperationId:   active.ID,
					OperationType: string(active.OperationType),
					State:         storeStatusToProto(active.Status),
					Actor:         active.Actor.UserID,
				}
			case lookupErr == nil, errors.Is(lookupErr, store.ErrNotFound):
				// No active operation is an expected observation.
			default:
				return nil, s.inventoryObservationInternal("get active inventory operation", lookupErr)
			}
		}
		rows = append(rows, row)
	}

	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		for _, pair := range [][2]string{
			{left.CustomerId, right.CustomerId},
			{left.ClusterId, right.ClusterId},
			{left.Namespace, right.Namespace},
			{left.ReleaseName, right.ReleaseName},
			{left.ReleaseDefinitionId, right.ReleaseDefinitionId},
		} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return false
	})

	return connect.NewResponse(&orchestratorv1.ListReleaseInventoryResponse{Rows: rows}), nil
}

func (s *Service) inventoryObservationInternal(operation string, err error) error {
	if s.logger != nil {
		s.logger.Error(operation, "error", err)
	}
	return connect.NewError(connect.CodeInternal, errors.New("unable to read release inventory"))
}
