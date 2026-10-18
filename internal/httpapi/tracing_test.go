package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/wallet/internal/store"
	"example.com/wallet/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestHTTPTracing(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer provider.Shutdown(context.Background())
	server := &Server{Store: &store.Store{Tracing: &telemetry.Tracing{Provider: provider}}}
	for _, path := range []string{"/health/live", "/wallets/private-wallet?secret=hidden", "/unknown-private-path"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, req)
	}
	spans := exporter.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("spans=%d", len(spans))
	}
	for i, span := range spans {
		if span.SpanContext.TraceID().String() != "0123456789abcdef0123456789abcdef" || span.Parent.SpanID().String() != "0123456789abcdef" {
			t.Fatal("lost remote parent")
		}
		for _, attr := range span.Attributes {
			if strings.Contains(attr.Value.AsString(), "private") || strings.Contains(attr.Value.AsString(), "hidden") {
				t.Fatal("sensitive URL in span")
			}
		}
		want := []string{"GET /health/live", "GET /wallets/{walletId}", "unmatched"}[i]
		if span.Name != want {
			t.Fatalf("route=%q, want %q", span.Name, want)
		}
	}
}
