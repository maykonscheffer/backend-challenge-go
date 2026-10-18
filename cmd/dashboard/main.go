// Command dashboard is a local-only viewer of the existing authenticated metrics.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var page string

type viewer struct {
	http    *http.Client
	bases   []string
	mu      sync.Mutex
	token   string
	expires time.Time
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func (v *viewer) access(ctx context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.token != "" && time.Now().Before(v.expires) {
		return v.token, nil
	}
	body := url.Values{"grant_type": {"client_credentials"}, "client_id": {"internal-service"}, "client_secret": {env("DASHBOARD_INTERNAL_SECRET", "local-internal-service-secret")}}
	r, err := http.NewRequestWithContext(ctx, "POST", env("TEST_TOKEN_URL", "http://localhost:8180/realms/wallet/protocol/openid-connect/token"), strings.NewReader(body.Encode()))
	if err != nil {
		return "", err
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := v.http.Do(r)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var token struct {
		Access  string `json:"access_token"`
		Seconds int    `json:"expires_in"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&token) != nil || token.Access == "" || token.Seconds <= 0 {
		return "", fmt.Errorf("authentication unavailable")
	}
	v.token = token.Access
	v.expires = time.Now().Add(time.Duration(max(0, token.Seconds-15)) * time.Second)
	return v.token, nil
}
func (v *viewer) metrics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	token, err := v.access(ctx)
	if err != nil {
		http.Error(w, "Autenticacao indisponivel", 503)
		return
	}
	values := map[string]float64{}
	for i, base := range v.bases {
		req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+"/metrics", nil)
		if err != nil {
			http.Error(w, "Configuracao invalida", 503)
			return
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := v.http.Do(req)
		if err != nil {
			http.Error(w, "API indisponivel", 503)
			return
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != 200 || readErr != nil {
			http.Error(w, "Metricas indisponiveis", 503)
			return
		}
		for _, line := range strings.Split(string(body), "\n") {
			f := strings.Fields(line)
			if len(f) != 2 || !strings.HasPrefix(f[0], "wallet_") {
				continue
			}
			n, err := strconv.ParseFloat(f[1], 64)
			if err != nil {
				continue
			}
			// These gauges come from the shared database; all other counters are per process.
			shared := strings.HasPrefix(f[0], "wallet_transactions{") || f[0] == "wallet_outbox_pending" || f[0] == "wallet_outbox_oldest_age_seconds"
			if !shared || i == 0 {
				values[f[0]] += n
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"at": time.Now().UTC(), "instances": len(v.bases), "metrics": values})
}
func (v *viewer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/metrics", v.metrics)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, page)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prevent other sites/DNS rebinding from using this authenticated local proxy.
		if r.Host != "127.0.0.1:8090" && r.Host != "localhost:8090" {
			http.Error(w, "Host forbidden", 403)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			http.Error(w, "Origin forbidden", 403)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		mux.ServeHTTP(w, r)
	})
}
func main() {
	v := &viewer{http: &http.Client{Timeout: 4 * time.Second}, bases: strings.Split(env("TEST_API_URLS", "http://localhost:8080,http://localhost:8081,http://localhost:8082"), ",")}
	server := &http.Server{Addr: "127.0.0.1:8090", Handler: v.handler(), ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	log.Print("Dashboard local: http://127.0.0.1:8090 (Ctrl+C para encerrar)")
	log.Fatal(server.ListenAndServe())
}
