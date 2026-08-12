package authorization

import (
	"context"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
	apitrace "go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/ndzuki/release-manager/internal/authorization"

// InstallTracing installs a local SDK provider and W3C trace-context propagation.
// Exporters remain deployment concerns; spans are still correlated locally and in tests.
func InstallTracing() func(context.Context) error {
	provider := trace.NewTracerProvider(
		trace.WithSampler(trace.ParentBased(trace.AlwaysSample())),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return provider.Shutdown
}

// TraceInterceptor extracts/injects W3C trace context and creates one span per Connect call.
func TraceInterceptor() connect.UnaryInterceptorFunc {
	propagator := otel.GetTextMapPropagator()
	tracer := otel.Tracer(instrumentationName)
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			kind := apitrace.SpanKindServer
			if req.Spec().IsClient {
				kind = apitrace.SpanKindClient
			} else {
				ctx = propagator.Extract(ctx, propagation.HeaderCarrier(req.Header()))
			}
			ctx, span := tracer.Start(ctx, req.Spec().Procedure, apitrace.WithSpanKind(kind))
			defer span.End()
			if req.Spec().IsClient {
				propagator.Inject(ctx, propagation.HeaderCarrier(req.Header()))
			}
			response, err := next(ctx, req)
			if err != nil {
				span.RecordError(err)
			}
			return response, err
		}
	}
}
