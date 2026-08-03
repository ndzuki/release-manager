package authorization

import (
	"context"
	"testing"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

// BenchmarkAuthorizeWrite measures the consumer-side governance write decision
// on a warm snapshot (no Auth RPC in the measured loop). REQ-027 NFR:
// AuthorizeWrite() p99 < 5ms excluding the Auth RPC.
func BenchmarkAuthorizeWrite(b *testing.B) {
	const (
		orgID      = "8d7560b5-3f7f-4cbe-9924-1e88656dd0f7"
		customerID = "f4652165-4726-42be-9bc6-fb046cf91a54"
	)
	handler := &snapshotHandler{response: &authv1.GetAuthorizationSnapshotResponse{
		OrganizationId: orgID, CustomerId: customerID, ActorId: "user-1", CanExecuteEmergency: true,
		SourceVersion: 1, PolicyVersion: 1, Checkpoint: 1, Fresh: true,
	}}
	module := newModuleFixtureBench(b, handler)
	actor := authctx.Actor{UserID: "user-1", OrganizationID: orgID}
	ctx := context.Background()
	// AuthorizeWrite creates the cache entry; the first decision fails closed
	// while catching up, and the second observes the settled snapshot.
	if err := module.AuthorizeWrite(ctx, actor, customerID, store.AuthorizationExecuteEmergency); err == nil {
		b.Fatal("expected first decision to fail closed")
	}
	if err := module.AuthorizeWrite(ctx, actor, customerID, store.AuthorizationExecuteEmergency); err != nil {
		b.Fatalf("warm decision failed: %v", err)
	}
	b.ResetTimer()
	for b.Loop() {
		if err := module.AuthorizeWrite(ctx, actor, customerID, store.AuthorizationExecuteEmergency); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSnapshotRPC measures the consumer-side pull of one snapshot through
// a local Connect server. REQ-027 NFR: snapshot RPC p99 < 50ms including the
// 200ms deadline. The fixture may start from an empty or a persisted
// checkpoint, so the loop is warmed until the snapshot is settled before
// timing.
func BenchmarkSnapshotRPC(b *testing.B) {
	const (
		orgID      = "8d7560b5-3f7f-4cbe-9924-1e88656dd0f7"
		customerID = "f4652165-4726-42be-9bc6-fb046cf91a54"
	)
	handler := &snapshotHandler{response: &authv1.GetAuthorizationSnapshotResponse{
		OrganizationId: orgID, CustomerId: customerID, ActorId: "user-1", CanExecuteEmergency: true,
		SourceVersion: 1, PolicyVersion: 1, Checkpoint: 1, Fresh: true,
	}}
	module := newModuleFixtureBench(b, handler)
	actor := authctx.Actor{UserID: "user-1", OrganizationID: orgID}
	ctx := context.Background()
	// AuthorizeWrite creates the cache entry; the first decision fails closed
	// while catching up, and the second observes the settled snapshot.
	if err := module.AuthorizeWrite(ctx, actor, customerID, store.AuthorizationExecuteEmergency); err == nil {
		b.Fatal("expected first decision to fail closed")
	}
	if err := module.AuthorizeWrite(ctx, actor, customerID, store.AuthorizationExecuteEmergency); err != nil {
		b.Fatalf("warm decision failed: %v", err)
	}
	key := cacheKey{actorID: "user-1", organizationID: orgID, customerID: customerID}
	b.ResetTimer()
	for b.Loop() {
		if err := module.pull(ctx, key); err != nil {
			b.Fatal(err)
		}
	}
}
