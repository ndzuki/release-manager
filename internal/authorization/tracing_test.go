package authorization

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
)

// TASK-246: TraceInterceptor wraps the authorization client, whose pull failures can
// carry credential text (see the note in module.go). A deployment that attaches an
// OTel exporter would ship that text, so the span must record only the bounded
// Connect code -- the same choice the WARN log makes (TASK-243). This drives a real
// client call so the assertion covers the interceptor as wired, and it is
// falsifiable: restoring span.RecordError(err) puts the marker into an exception
// event and fails the Events assertion below.
func TestTraceInterceptorRecordsBoundedErrorOnly(t *testing.T) {
	const sensitive = "bearer secret-token-abc123"

	mux := http.NewServeMux()
	path, handler := authv1connect.NewAuthorizationServiceHandler(&snapshotHandler{
		err: connect.NewError(connect.CodeUnauthenticated, errors.New(sensitive)),
	})
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(context.Background()))
		otel.SetTracerProvider(previousProvider)
	})

	client := authv1connect.NewAuthorizationServiceClient(server.Client(), server.URL,
		connect.WithInterceptors(TraceInterceptor()))
	_, err := client.GetAuthorizationSnapshot(context.Background(), connect.NewRequest(&authv1.GetAuthorizationSnapshotRequest{
		OrganizationId: staleOrgID,
		CustomerId:     staleCustomerID,
	}))
	require.Error(t, err)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1, "the interceptor creates exactly one client span per call")
	span := spans[0]

	assert.Empty(t, span.Events, "no exception event may carry the raw error text")
	assert.Equal(t, codes.Error, span.Status.Code)
	assert.Equal(t, "unauthenticated", span.Status.Description, "the status description is the bounded code")
	assert.NotContains(t, span.Name, sensitive)

	var codesSeen []string
	for _, attr := range span.Attributes {
		assert.NotContains(t, attr.Value.AsString(), sensitive)
		if attr.Key == attribute.Key("error.code") {
			codesSeen = append(codesSeen, attr.Value.AsString())
		}
	}
	assert.Equal(t, []string{"unauthenticated"}, codesSeen)
}
