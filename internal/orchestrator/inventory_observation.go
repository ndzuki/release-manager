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
	_ *connect.Request[orchestratorv1.ListReleaseInventoryRequest],
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

	rows := make([]*orchestratorv1.ReleaseInventoryRow, 0, len(items))
	cache := newInventoryNameCache()
	for _, item := range items {
		if item == nil {
			continue
		}
		if _, visible := visibleCustomers[item.CustomerID]; !visible {
			continue
		}
		row, err := s.inventoryRow(ctx, item, cache)
		if err != nil {
			return nil, err
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

// inventoryNameCache memoizes the display-name lookups shared across rows so a
// large inventory does not repeat the same reads. A lookup that finds nothing is
// deliberately not cached: the negative result is retried per row, matching the
// behaviour this replaced.
type inventoryNameCache struct {
	customers   map[string]string
	clusters    map[string]string
	definitions map[string]string
}

func newInventoryNameCache() *inventoryNameCache {
	return &inventoryNameCache{
		customers:   make(map[string]string),
		clusters:    make(map[string]string),
		definitions: make(map[string]string),
	}
}

// inventoryRow builds one response row, resolving the display names it carries
// and the active operation it references.
func (s *Service) inventoryRow(
	ctx context.Context,
	item *store.ReleaseInventory,
	cache *inventoryNameCache,
) (*orchestratorv1.ReleaseInventoryRow, error) {
	row := &orchestratorv1.ReleaseInventoryRow{
		CustomerId:          item.CustomerID,
		ClusterId:           item.ClusterID,
		ReleaseDefinitionId: item.ReleaseDefinitionID,
		Namespace:           item.Namespace,
		ReleaseName:         item.ReleaseName,
		Revision:            int32(item.Revision), //nolint:gosec // release revisions are bounded positive integers
		Status:              inventoryStatusToProto(item.InventoryStatus),
	}

	customerName, err := s.cachedName(ctx, cache.customers, item.CustomerID, "get inventory customer",
		func(ctx context.Context, id string) (string, bool, error) {
			customer, err := s.store.Customers().Get(ctx, id)
			return resolveName(customer, err, func(c *store.Customer) string { return c.Name })
		})
	if err != nil {
		return nil, err
	}
	row.CustomerName = customerName

	clusterName, err := s.cachedName(ctx, cache.clusters, item.ClusterID, "get inventory cluster",
		func(ctx context.Context, id string) (string, bool, error) {
			cluster, err := s.store.Clusters().Get(ctx, id)
			return resolveName(cluster, err, func(c *store.Cluster) string { return c.Name })
		})
	if err != nil {
		return nil, err
	}
	row.ClusterName = clusterName

	if item.ReleaseDefinitionID == "" {
		return row, nil
	}
	definitionName, err := s.cachedName(ctx, cache.definitions, item.ReleaseDefinitionID, "get inventory release definition",
		func(ctx context.Context, id string) (string, bool, error) {
			definition, err := s.store.Definitions().Get(ctx, id)
			return resolveName(definition, err, func(d *store.ReleaseDefinition) string { return d.Name })
		})
	if err != nil {
		return nil, err
	}
	row.ReleaseDefinitionName = definitionName
	if err := s.applyActiveOperation(ctx, row, item.ReleaseDefinitionID); err != nil {
		return nil, err
	}
	return row, nil
}

// nameResolver reads one entity's display name. found is false when the record
// does not exist, which is an expected absence rather than an error.
type nameResolver func(ctx context.Context, id string) (name string, found bool, err error)

// resolveName adapts a store Get into a name lookup: a missing record is an
// expected absence, not an error.
func resolveName[T any](entity *T, getErr error, name func(*T) string) (result string, found bool, err error) {
	if getErr != nil {
		if errors.Is(getErr, store.ErrNotFound) {
			return "", false, nil
		}
		return "", false, getErr
	}
	if entity == nil {
		return "", false, nil
	}
	return name(entity), true, nil
}

// cachedName resolves a display name through resolve, memoizing per id.
//
// An absent record resolves to the empty name rather than an error, and that
// negative result is deliberately NOT cached: the next row retries the read.
// That matches the behaviour this replaced.
func (s *Service) cachedName(
	ctx context.Context,
	cache map[string]string,
	id, operation string,
	resolve nameResolver,
) (string, error) {
	if id == "" {
		return "", nil
	}
	if _, cached := cache[id]; !cached {
		name, found, err := resolve(ctx, id)
		if err != nil {
			return "", s.inventoryObservationInternal(operation, err)
		}
		if found {
			cache[id] = name
		}
	}
	return cache[id], nil
}

// applyActiveOperation attaches the non-terminal operation a definition currently
// has, if any. A definition with no active operation is an expected observation.
func (s *Service) applyActiveOperation(
	ctx context.Context,
	row *orchestratorv1.ReleaseInventoryRow,
	definitionID string,
) error {
	active, err := s.store.Operations().GetActiveForDefinition(ctx, definitionID)
	switch {
	case err == nil && active != nil && !active.Status.IsTerminal():
		row.ActiveOperation = &orchestratorv1.ActiveOperationRef{
			OperationId:   active.ID,
			OperationType: string(active.OperationType),
			State:         storeStatusToProto(active.Status),
			Actor:         active.Actor.UserID,
		}
	case err == nil, errors.Is(err, store.ErrNotFound):
		// No active operation is an expected observation.
	default:
		return s.inventoryObservationInternal("get active inventory operation", err)
	}
	return nil
}

func (s *Service) inventoryObservationInternal(operation string, err error) error {
	if s.logger != nil {
		s.logger.Error(operation, "error", err)
	}
	return connect.NewError(connect.CodeInternal, errors.New("unable to read release inventory"))
}
