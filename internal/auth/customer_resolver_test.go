package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestStoreCustomerResolver_ResolveAndNotFound(t *testing.T) {
	st, err := sqlitestore.Open("file:" + uuid.New().String() + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	resolver := auth.NewStoreCustomerResolver(st)
	if _, err := resolver.Resolve(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown customer: err = %v, want store.ErrNotFound", err)
	}

	customer := &store.Customer{ID: "c-1", Name: "Acme", Slug: "acme", Status: store.CustomerDisabled}
	if err := st.Customers().Create(ctx, customer); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	got, err := resolver.Resolve(ctx, "c-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Status != store.CustomerDisabled {
		t.Fatalf("status = %q, want %q", got.Status, store.CustomerDisabled)
	}
}
