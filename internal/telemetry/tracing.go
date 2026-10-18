// Package telemetry contains optional, bounded, best-effort tracing.
package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/fx"
)

type Tracing struct{ Provider trace.TracerProvider }

func New(lc fx.Lifecycle) (*Tracing, error) {
	enabled := false
	if value := os.Getenv("OTEL_ENABLED"); value != "" {
		var err error
		enabled, err = strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("OTEL_ENABLED must be a boolean")
		}
	}
	if !enabled {
		return &Tracing{}, nil
	}
	// The exporter connects asynchronously; collector availability is not readiness.
	exporter, err := otlptracehttp.New(context.Background(), otlptracehttp.WithTimeout(time.Second), otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}))
	if err != nil {
		return nil, err
	}
	name := os.Getenv("OTEL_SERVICE_NAME")
	if name == "" {
		name = "wallet-api"
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", name))),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithBatcher(exporter, sdktrace.WithMaxQueueSize(512), sdktrace.WithMaxExportBatchSize(128), sdktrace.WithBatchTimeout(time.Second), sdktrace.WithExportTimeout(time.Second)),
	)
	lc.Append(fx.Hook{OnStop: func(ctx context.Context) error {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := provider.Shutdown(bounded); err != nil {
			slog.Warn("trace flush incomplete")
		}
		return nil
	}})
	return &Tracing{Provider: provider}, nil
}

func (t *Tracing) Start(ctx context.Context, name string, kind trace.SpanKind) (context.Context, trace.Span) {
	var provider trace.TracerProvider = noop.NewTracerProvider()
	if t != nil && t.Provider != nil {
		provider = t.Provider
	}
	return provider.Tracer("example.com/wallet").Start(ctx, name, trace.WithSpanKind(kind))
}

// Only W3C trace context is accepted: no baggage, credentials or request bodies.
func Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	return propagation.TraceContext{}.Extract(ctx, carrier)
}
func Inject(ctx context.Context) propagation.MapCarrier {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier
}
