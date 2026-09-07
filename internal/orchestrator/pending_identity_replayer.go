package orchestrator

import (
	"context"
)

// PendingIdentityReplayer is the minimal seam through which SyncInventory
// triggers the REQ-088 event-driven replay of a buffered workload identity
// right after its release_inventory row is created (D5=A). The operator
// Service implements it in the combined deployment (cmd/orchestrator mounts
// both services on one store); tests inject the same in-process service. A nil
// replayer keeps SyncInventory's established semantics unchanged (replay is a
// best-effort convergence step, never part of the sync success contract).
type PendingIdentityReplayer interface {
	ReplayAfterInventory(ctx context.Context, customerID, clusterID, namespace, releaseName string) error
}

// operatorPendingIdentityReplayer adapts the in-process operator Service into
// the PendingIdentityReplayer seam. The concrete unexported type keeps the
// NewService variadic type-switch unambiguous: a value returned by
// NewPendingIdentityReplayer can only match the PendingIdentityReplayer case,
// never the (structurally overlapping) emergency dispatcher case.
type operatorPendingIdentityReplayer struct {
	service PendingIdentityReplayer
}

// NewPendingIdentityReplayer adapts a ReplayAfterInventory provider (the
// mounted operator Service in cmd/orchestrator) into the SyncInventory seam.
// A nil provider yields nil, keeping SyncInventory semantics unchanged.
func NewPendingIdentityReplayer(service PendingIdentityReplayer) PendingIdentityReplayer {
	if service == nil {
		return nil
	}
	return &operatorPendingIdentityReplayer{service: service}
}

func (r *operatorPendingIdentityReplayer) ReplayAfterInventory(ctx context.Context, customerID, clusterID, namespace, releaseName string) error {
	return r.service.ReplayAfterInventory(ctx, customerID, clusterID, namespace, releaseName)
}
