package app

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestMaintenanceInterceptor(t *testing.T) {
	const (
		readProcedure  = "/test.v1.Service/Get"
		writeProcedure = "/test.v1.Service/Create"
	)
	tests := []struct {
		name      string
		enabled   bool
		procedure string
		wantCode  connect.Code
	}{
		{name: "write rejected", enabled: true, procedure: writeProcedure, wantCode: connect.CodeUnavailable},
		{name: "read allowed", enabled: true, procedure: readProcedure, wantCode: connect.CodeUnknown},
		{name: "disabled allows write", enabled: false, procedure: writeProcedure, wantCode: connect.CodeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interceptor := MaintenanceInterceptor(tt.enabled, map[string]struct{}{readProcedure: {}}, nil)
			handler := connect.NewUnaryHandler(
				tt.procedure,
				func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
					return connect.NewResponse(&emptypb.Empty{}), nil
				},
				connect.WithInterceptors(interceptor),
			)
			server := httptest.NewServer(handler)
			t.Cleanup(server.Close)
			client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, server.URL+tt.procedure)

			_, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
			if tt.wantCode == connect.CodeUnknown {
				require.NoError(t, err)
				return
			}
			require.Equal(t, tt.wantCode, connect.CodeOf(err))
		})
	}
}

func TestMaintenanceInterceptorLogsRejectedProcedure(t *testing.T) {
	const procedure = "/test.v1.Service/Create"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	interceptor := MaintenanceInterceptor(true, nil, logger)
	handler := connect.NewUnaryHandler(
		procedure,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			return connect.NewResponse(&emptypb.Empty{}), nil
		},
		connect.WithInterceptors(interceptor),
	)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](server.Client(), server.URL+procedure)

	_, err := client.CallUnary(t.Context(), connect.NewRequest(&emptypb.Empty{}))
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.Contains(t, logs.String(), procedure)
	require.Contains(t, logs.String(), "maintenance write rejected")
}
