package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLocalAccessBoundary(t *testing.T) {
	v := &viewer{}
	for _, tc := range []struct {
		host, origin string
		want         int
	}{{"evil.example:8090", "", 403}, {"127.0.0.1:8090", "https://evil.example", 403}, {"127.0.0.1:8090", "", 200}} {
		r := httptest.NewRequest("GET", "http://"+tc.host+"/", nil)
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		v.handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatal(tc, w.Code)
		}
		if tc.want == 200 && (!strings.Contains(w.Body.String(), "/api/metrics") || w.Header().Get("Cache-Control") != "no-store") {
			t.Fatal("missing dashboard or caching protection")
		}
	}
}

func TestLiveDashboardMetrics(t *testing.T) {
	if os.Getenv("TEST_API_URLS") == "" {
		t.Skip("requires real Compose services")
	}
	v := &viewer{http: &http.Client{Timeout: 4 * time.Second}, bases: strings.Split(os.Getenv("TEST_API_URLS"), ",")}
	r := httptest.NewRequest("GET", "http://127.0.0.1:8090/api/metrics", nil)
	w := httptest.NewRecorder()
	v.handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("real metrics unavailable", w.Code, w.Body.String())
	}
	var payload struct {
		Instances int                `json:"instances"`
		Metrics   map[string]float64 `json:"metrics"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Instances != 3 {
		t.Fatal("expected three APIs")
	}
	for _, name := range []string{"wallet_outbox_pending", "wallet_http_duration_seconds_sum", "wallet_sql_concurrency_conflicts_total"} {
		if _, ok := payload.Metrics[name]; !ok {
			t.Fatal("missing metric", name)
		}
	}
	if v.token == "" || strings.Contains(w.Body.String(), v.token) || strings.Contains(w.Body.String(), "local-internal-service-secret") {
		t.Fatal("credential leakage or failed auth")
	}
	first := v.token
	w = httptest.NewRecorder()
	v.handler().ServeHTTP(w, r)
	if w.Code != 200 || v.token != first {
		t.Fatal("token cache failed")
	}
}
