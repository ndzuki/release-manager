package interceptor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/contracts"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

const probeProcedure = "/fake.ProbeService/Call"

// probeHandler exercises the full interceptor chain over HTTP:
//   - X-Probe: "internal"  -> handler fails with a CodeInternal error whose
//     message carries SQL text that must never reach the client (AC-010-04).
//   - X-Probe: "notfound"  -> handler fails with a stable business error.
//   - otherwise            -> handler records an idempotency record through
//     the real sqlite store and returns a success response.
func probeHandler(t *testing.T, st store.Store) func(context.Context, *connect.Request[commonv1.Pagination]) (*connect.Response[commonv1.Pagination], error) {
	t.Helper()
	return func(ctx context.Context, req *connect.Request[commonv1.Pagination]) (*connect.Response[commonv1.Pagination], error) {
		switch req.Header().Get("X-Probe") {
		case "internal":
			return nil, connect.NewError(connect.CodeInternal,
				errors.New("query audit events: SELECT * FROM credentials WHERE secret='boom'"))
		case "notfound":
			return nil, connect.NewError(connect.CodeNotFound, errors.New("customer not found"))
		default:
			key := req.Header().Get("Idempotency-Key")
			if key == "" {
				return connect.NewResponse(&commonv1.Pagination{PageSize: 20}), nil
			}
			record := &store.IdempotencyRecord{
				Scope:       "integration",
				Key:         "key-" + key,
				RequestHash: "hash-1",
				ExpiresAt:   time.Now().UTC().Add(time.Hour),
			}
			_, created, err := st.Idempotency().CreateOrGet(ctx, record)
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, err)
			}
			if !created {
				return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("idempotency replay detected"))
			}
			return connect.NewResponse(&commonv1.Pagination{PageSize: 20}), nil
		}
	}
}

// newChainServer boots an httptest server with the real interceptor chain
// [RequestID, ErrorSanitize] around the probe handler, mirroring the wiring
// in cmd/*/main.go. It returns the base URL.
func newChainServer(t *testing.T, st store.Store) string {
	t.Helper()

	handler := connect.NewUnaryHandler[commonv1.Pagination, commonv1.Pagination](
		probeProcedure,
		probeHandler(t, st),
		connect.WithInterceptors(
			NewRequestIDInterceptor(nil),
			NewErrorSanitizeInterceptor(nil),
		),
	)

	mux := http.NewServeMux()
	mux.Handle(probeProcedure, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func newProbeClient(t *testing.T, baseURL string) *connect.Client[commonv1.Pagination, commonv1.Pagination] {
	t.Helper()
	return connect.NewClient[commonv1.Pagination, commonv1.Pagination](
		http.DefaultClient,
		baseURL+probeProcedure,
	)
}

func TestChainRequestIDEcho(t *testing.T) {
	st, err := sqlitestore.Open(t.TempDir() + "/chain.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	client := newProbeClient(t, newChainServer(t, st))

	t.Run("echoes client-supplied header", func(t *testing.T) {
		req := connect.NewRequest(&commonv1.Pagination{PageSize: 10})
		req.Header().Set("X-Request-ID", "req-42")
		resp, err := client.CallUnary(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, "req-42", resp.Header().Get("X-Request-ID"))
	})

	t.Run("generates UUID when header absent", func(t *testing.T) {
		resp, err := client.CallUnary(context.Background(), connect.NewRequest(&commonv1.Pagination{}))
		require.NoError(t, err)
		rid := resp.Header().Get("X-Request-ID")
		require.NotEmpty(t, rid)
		_, err = uuid.Parse(rid)
		assert.NoError(t, err, "generated request id should be a UUID")
	})
}

func TestChainErrorSanitization(t *testing.T) {
	st, err := sqlitestore.Open(t.TempDir() + "/chain.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	client := newProbeClient(t, newChainServer(t, st))

	t.Run("internal error leaks no SQL and carries request_id", func(t *testing.T) {
		req := connect.NewRequest(&commonv1.Pagination{})
		req.Header().Set("X-Request-ID", "req-internal")
		req.Header().Set("X-Probe", "internal")

		_, err := client.CallUnary(context.Background(), req)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))

		var connectErr *connect.Error
		require.True(t, errors.As(err, &connectErr))
		assert.Equal(t, "internal error", connectErr.Message())
		assert.NotContains(t, err.Error(), "SELECT")
		assert.NotContains(t, err.Error(), "credentials")
		assert.Equal(t, "req-internal", connectErr.Meta().Get(contracts.RequestIDHeader))
	})

	t.Run("business error keeps stable code and message", func(t *testing.T) {
		req := connect.NewRequest(&commonv1.Pagination{})
		req.Header().Set("X-Probe", "notfound")

		_, err := client.CallUnary(context.Background(), req)
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

		var connectErr *connect.Error
		require.True(t, errors.As(err, &connectErr))
		assert.Equal(t, "customer not found", connectErr.Message())
		assert.NotEmpty(t, connectErr.Meta().Get(contracts.RequestIDHeader))
	})
}

func TestChainIdempotencyReplayOverRealStore(t *testing.T) {
	st, err := sqlitestore.Open(t.TempDir() + "/chain.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	client := newProbeClient(t, newChainServer(t, st))

	// First call creates the idempotency record.
	req := connect.NewRequest(&commonv1.Pagination{})
	req.Header().Set("Idempotency-Key", "replay-key")
	_, err = client.CallUnary(context.Background(), req)
	require.NoError(t, err)

	// Same key replays against the stored record: no duplicate creation.
	_, err = client.CallUnary(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}
