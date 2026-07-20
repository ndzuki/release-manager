package auth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/orchestrator"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestConnectCustomerResolver_ResolveIncludesDisabledStatus(t *testing.T) {
	st, err := sqlitestore.Open("file:resolver-" + t.Name() + "?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	customer := &store.Customer{
		ID:     "customer-disabled",
		Name:   "Disabled Customer",
		Slug:   "disabled-customer",
		Status: store.CustomerDisabled,
	}
	require.NoError(t, st.Customers().Create(context.Background(), customer))

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := orchestrator.NewService(st, nil, "staging", logger)
	path, handler := orchestratorv1connect.NewOrchestratorServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := orchestratorv1connect.NewOrchestratorServiceClient(server.Client(), server.URL)
	resolver := NewConnectCustomerResolver(client)
	resolved, err := resolver.Resolve(context.Background(), customer.ID)
	require.NoError(t, err)
	assert.Equal(t, customer.ID, resolved.ID)
	assert.Equal(t, store.CustomerDisabled, resolved.Status)
}
