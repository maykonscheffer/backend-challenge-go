package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx/fxtest"
)

func TestCollectorFailureDoesNotBlock(t *testing.T) {
	var requests atomic.Int64
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(503) }))
	defer collector.Close()
	t.Setenv("OTEL_ENABLED", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
	lifecycle := fxtest.NewLifecycle(t)
	tracing, err := New(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.RequireStart()
	start := time.Now()
	for i := 0; i < 2000; i++ {
		_, span := tracing.Start(context.Background(), "test", trace.SpanKindInternal)
		span.End()
	}
	if time.Since(start) > time.Second {
		t.Fatal("span production blocked by collector")
	}
	lifecycle.RequireStop()
	if requests.Load() == 0 {
		t.Fatal("export failure was not exercised")
	}
}

func TestDefaultDisabled(t *testing.T) {
	t.Setenv("OTEL_ENABLED", "")
	tracing, err := New(fxtest.NewLifecycle(t))
	if err != nil {
		t.Fatal(err)
	}
	_, span := tracing.Start(context.Background(), "disabled", trace.SpanKindInternal)
	defer span.End()
	if span.IsRecording() || span.SpanContext().IsValid() {
		t.Fatal("disabled tracing recorded a span")
	}
}
