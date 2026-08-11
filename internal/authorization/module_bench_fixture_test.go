package authorization

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// newModuleFixtureBench mirrors newModuleFixture for *testing.B.
func newModuleFixtureBench(b *testing.B, handler *snapshotHandler) *Module {
	b.Helper()
	mux := http.NewServeMux()
	path, rpcHandler := authv1connect.NewAuthorizationServiceHandler(handler)
	mux.Handle(path, rpcHandler)
	server := httptest.NewServer(mux)
	b.Cleanup(server.Close)
	st := sqlitestore.OpenTest(b)
	client := authv1connect.NewAuthorizationServiceClient(server.Client(), server.URL)
	return NewModule(client, st.Authorization(), NewMetrics(prometheus.NewRegistry()), slog.New(slog.DiscardHandler), time.Second, 30*time.Second)
}

