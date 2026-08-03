package authorization

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// TestPullWarmupStress verifies the catch-up contract on a fresh scope:
// the first decision fails closed, the next decision on the same scope
// observes the fresh snapshot and allows.
func TestPullWarmupStress(t *testing.T) {
	const (
		orgID      = "8d7560b5-3f7f-4cbe-9924-1e88656dd0f7"
		customerID = "f4652165-4726-42be-9bc6-fb046cf91a54"
	)
	handler := &snapshotHandler{response: &authv1.GetAuthorizationSnapshotResponse{
		OrganizationId: orgID, CustomerId: customerID, ActorId: "user-1", CanExecuteEmergency: true,
		SourceVersion: 1, PolicyVersion: 1, Checkpoint: 1, Fresh: true,
	}}
	mux := http.NewServeMux()
	path, rpcHandler := authv1connect.NewAuthorizationServiceHandler(handler)
	mux.Handle(path, rpcHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := authv1connect.NewAuthorizationServiceClient(server.Client(), server.URL)
	actor := authctx.Actor{UserID: "user-1", OrganizationID: orgID}
	ctx := context.Background()

	for iteration := range 50 {
		st := sqlitestore.OpenTest(t)
		module := NewModule(client, st.Authorization(), NewMetrics(prometheus.NewRegistry()), slog.New(slog.DiscardHandler), time.Second, 30*time.Second)
		// First decision on a fresh scope must fail closed while catching up.
		err1 := module.AuthorizeWrite(ctx, actor, customerID, store.AuthorizationExecuteEmergency)
		if err1 == nil {
			t.Fatalf("iteration %d: first call unexpectedly allowed before catch-up", iteration)
		}
		// The next decision must observe the caught-up snapshot.
		err2 := module.AuthorizeWrite(ctx, actor, customerID, store.AuthorizationExecuteEmergency)
		if err2 != nil {
			t.Fatalf("iteration %d: warm call failed: %v", iteration, err2)
		}
	}
}

// TestPullConcurrentWarmup warms the module once, then verifies concurrent
// decisions on the fresh snapshot all allow.
func TestPullConcurrentWarmup(t *testing.T) {
	const (
		orgID      = "8d7560b5-3f7f-4cbe-9924-1e88656dd0f7"
		customerID = "f4652165-4726-42be-9bc6-fb046cf91a54"
	)
	handler := &snapshotHandler{response: &authv1.GetAuthorizationSnapshotResponse{
		OrganizationId: orgID, CustomerId: customerID, ActorId: "user-1", CanExecuteEmergency: true,
		SourceVersion: 1, PolicyVersion: 1, Checkpoint: 1, Fresh: true,
	}}
	mux := http.NewServeMux()
	path, rpcHandler := authv1connect.NewAuthorizationServiceHandler(handler)
	mux.Handle(path, rpcHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	st := sqlitestore.OpenTest(t)
	client := authv1connect.NewAuthorizationServiceClient(server.Client(), server.URL)
	module := NewModule(client, st.Authorization(), NewMetrics(prometheus.NewRegistry()), slog.New(slog.DiscardHandler), time.Second, 30*time.Second)
	actor := authctx.Actor{UserID: "user-1", OrganizationID: orgID}
	ctx := context.Background()

	// Warm the module so every concurrent decision starts from a fresh snapshot.
	if err := module.AuthorizeWrite(ctx, actor, customerID, store.AuthorizationExecuteEmergency); err == nil {
		t.Fatal("expected first decision to fail closed")
	}
	if err := module.AuthorizeWrite(ctx, actor, customerID, store.AuthorizationExecuteEmergency); err != nil {
		t.Fatalf("warm decision failed: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if err := module.AuthorizeWrite(ctx, actor, customerID, store.AuthorizationExecuteEmergency); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent warm call failed: %v", err)
	}
}
