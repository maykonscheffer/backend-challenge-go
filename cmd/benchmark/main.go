// Command benchmark drives the public HTTP API; it never writes directly to SQL.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"example.com/wallet/internal/domain"
	"example.com/wallet/internal/store"
	"github.com/google/uuid"
)

type settings struct {
	Requests       int `json:"requests"`
	Concurrency    int `json:"concurrency"`
	Wallets        int `json:"wallets"`
	DuplicateEvery int `json:"duplicateEvery"`
}
type report struct {
	RunID          string             `json:"runId"`
	Started        time.Time          `json:"startedAt"`
	Settings       settings           `json:"settings"`
	Environment    map[string]any     `json:"environment"`
	Duration       float64            `json:"durationSeconds"`
	Throughput     float64            `json:"requestsPerSecond"`
	Latency        map[string]float64 `json:"latencyMilliseconds"`
	Status         map[int]int        `json:"httpStatusCounts"`
	Failures       int                `json:"failedRequests"`
	Replays        int                `json:"replays"`
	Unique         int                `json:"expectedUniqueTransactions"`
	IdentityErrors int                `json:"transactionIdentityErrors"`
	MetricsDelta   map[string]float64 `json:"metricsDelta"`
	Outbox         map[string]float64 `json:"outboxSamples"`
	MetricsErrors  int                `json:"metricsErrors"`
	Reconciled     int                `json:"reconciledWallets"`
	Integrity      bool               `json:"integrityPassed"`
}
type client struct {
	http               *http.Client
	bases              []string
	internal, provider string
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func (c *client) call(ctx context.Context, base, method, path, token, key string, body any) (int, []byte, error) {
	var b []byte
	var err error
	if body != nil {
		b, err = json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return 0, nil, fmt.Errorf("HTTP transport failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, data, err
}
func token(ctx context.Context, h *http.Client, endpoint, id, secret string) (string, error) {
	r, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(url.Values{"grant_type": {"client_credentials"}, "client_id": {id}, "client_secret": {secret}}.Encode()))
	if err != nil {
		return "", err
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := h.Do(r)
	if err != nil {
		return "", fmt.Errorf("IdP unavailable")
	}
	defer resp.Body.Close()
	var v struct {
		Access string `json:"access_token"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v) != nil || v.Access == "" {
		return "", fmt.Errorf("IdP refused client credentials")
	}
	return v.Access, nil
}

var counterNames = []string{"wallet_sql_concurrency_conflicts_total", "wallet_sql_retries_total", "wallet_http_conflicts_total", "wallet_http_replays_total"}

func (c *client) metrics(ctx context.Context) (map[string]float64, error) {
	values := map[string]float64{}
	for index, base := range c.bases {
		status, b, err := c.call(ctx, base, "GET", "/metrics", c.internal, "", nil)
		if err != nil || status != 200 {
			return nil, fmt.Errorf("metrics unavailable")
		}
		found := map[string]bool{}
		for _, line := range strings.Split(string(b), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			name := fields[0]
			wanted := false
			for _, n := range counterNames {
				if name == n {
					wanted = true
				}
			}
			gauge := name == "wallet_outbox_pending" || name == "wallet_outbox_oldest_age_seconds"
			if !wanted && !gauge {
				continue
			}
			v, e := strconv.ParseFloat(fields[1], 64)
			if e != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, fmt.Errorf("invalid metric")
			}
			found[name] = true
			// Gauges describe the same shared database; never sum them across APIs.
			if wanted {
				values[name] += v
			} else if index == 0 {
				values[name] = v
			}
		}
		for _, name := range append(append([]string{}, counterNames...), "wallet_outbox_pending", "wallet_outbox_oldest_age_seconds") {
			if !found[name] {
				return nil, fmt.Errorf("required metric absent: %s", name)
			}
		}
	}
	return values, nil
}

type job struct {
	operation domain.Operation
	key       string
}
type observation struct {
	job     job
	status  int
	result  store.Result
	elapsed float64
	failed  bool
}

func percentile(sorted []float64, p float64) float64 {
	return sorted[int(math.Ceil(float64(len(sorted))*p))-1]
}

func run(cfg settings, output string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	bases := strings.Split(env("TEST_API_URLS", "http://localhost:8080,http://localhost:8081,http://localhost:8082"), ",")
	for i, b := range bases {
		bases[i] = strings.TrimRight(strings.TrimSpace(b), "/")
		u, e := url.Parse(bases[i])
		if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid API URL")
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = cfg.Concurrency + 4
	transport.MaxIdleConns = (cfg.Concurrency + 4) * len(bases)
	defer transport.CloseIdleConnections()
	c := &client{http: &http.Client{Timeout: 20 * time.Second, Transport: transport}, bases: bases}
	endpoint := env("TEST_TOKEN_URL", "http://localhost:8180/realms/wallet/protocol/openid-connect/token")
	var err error
	c.internal, err = token(ctx, c.http, endpoint, "internal-service", env("BENCH_INTERNAL_SECRET", "local-internal-service-secret"))
	if err != nil {
		return err
	}
	c.provider, err = token(ctx, c.http, endpoint, "provider-a", env("BENCH_PROVIDER_SECRET", "local-provider-a-secret"))
	if err != nil {
		return err
	}
	for _, base := range bases {
		status, _, e := c.call(ctx, base, "GET", "/health/ready", "", "", nil)
		if e != nil || status != 200 {
			return fmt.Errorf("API not ready")
		}
	}
	r := report{RunID: uuid.NewString(), Settings: cfg, Status: map[int]int{}, MetricsDelta: map[string]float64{}, Outbox: map[string]float64{}, Environment: map[string]any{"go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "visibleCPUs": runtime.NumCPU(), "gomaxprocs": runtime.GOMAXPROCS(0), "apiInstances": len(bases), "model": "closed-loop HTTP; BET 1.00 BRL; no automatic retry"}}
	wallets := make([]store.Wallet, cfg.Wallets)
	for i := range wallets {
		status, b, e := c.call(ctx, bases[i%len(bases)], "POST", "/wallets", c.internal, "", map[string]any{"playerId": uuid.NewString(), "initialBalance": domain.MoneyJSON{Amount: "1000000.00", Currency: "BRL"}})
		if e != nil || status != 201 || json.Unmarshal(b, &wallets[i]) != nil || wallets[i].ID == "" {
			return fmt.Errorf("benchmark wallet creation failed")
		}
	}
	jobs := make(chan job, cfg.Requests)
	expected := map[string]int{}
	var last job
	for i := 0; i < cfg.Requests; i++ {
		if cfg.DuplicateEvery > 0 && (i+1)%cfg.DuplicateEvery == 0 {
			jobs <- last
			continue
		}
		w := wallets[r.Unique%len(wallets)]
		last = job{operation: domain.Operation{ProviderID: "provider-a", ExternalID: uuid.NewString(), PlayerID: w.PlayerID, WalletID: w.ID, RoundID: r.RunID, GameID: "benchmark", Kind: "BET", Money: domain.MoneyJSON{Amount: "1.00", Currency: "BRL"}}, key: uuid.NewString()}
		expected[w.ID]++
		r.Unique++
		jobs <- last
	}
	close(jobs)
	before, err := c.metrics(ctx)
	if err != nil {
		return err
	}
	r.Outbox["pendingBefore"] = before["wallet_outbox_pending"]
	r.Outbox["maxSampledPending"] = before["wallet_outbox_pending"]
	r.Outbox["maxSampledLagSeconds"] = before["wallet_outbox_oldest_age_seconds"]
	stopSample := make(chan struct{})
	sampleDone := make(chan struct{})
	go func() {
		defer close(sampleDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopSample:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				m, e := c.metrics(ctx)
				if e != nil {
					r.MetricsErrors++
					continue
				}
				r.Outbox["maxSampledPending"] = math.Max(r.Outbox["maxSampledPending"], m["wallet_outbox_pending"])
				r.Outbox["maxSampledLagSeconds"] = math.Max(r.Outbox["maxSampledLagSeconds"], m["wallet_outbox_oldest_age_seconds"])
			}
		}
	}()
	results := make(chan observation, cfg.Requests)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for worker := 0; worker < cfg.Concurrency; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			sequence := worker
			for j := range jobs {
				begin := time.Now()
				status, b, e := c.call(ctx, bases[sequence%len(bases)], "POST", "/wagering/transactions", c.provider, j.key, j.operation)
				sequence++
				v := observation{job: j, status: status, elapsed: float64(time.Since(begin).Microseconds()) / 1000}
				v.failed = e != nil || status != 200 || json.Unmarshal(b, &v.result) != nil || v.result.Status != "PROCESSED" || v.result.TransactionID == ""
				results <- v
			}
		}(worker)
	}
	r.Started = time.Now().UTC()
	begin := time.Now()
	close(start)
	wg.Wait()
	r.Duration = time.Since(begin).Seconds()
	close(results)
	close(stopSample)
	<-sampleDone
	latencies := make([]float64, 0, cfg.Requests)
	identities := map[string]string{}
	for v := range results {
		latencies = append(latencies, v.elapsed)
		r.Status[v.status]++
		if v.failed {
			r.Failures++
			continue
		}
		if v.result.Replay {
			r.Replays++
		}
		if id, ok := identities[v.job.key]; ok && id != v.result.TransactionID {
			r.IdentityErrors++
		}
		identities[v.job.key] = v.result.TransactionID
	}
	sort.Float64s(latencies)
	r.Latency = map[string]float64{"p50": percentile(latencies, .50), "p95": percentile(latencies, .95), "p99": percentile(latencies, .99), "max": latencies[len(latencies)-1]}
	r.Throughput = float64(cfg.Requests) / r.Duration
	after, e := c.metrics(ctx)
	if e != nil {
		r.MetricsErrors++
	} else {
		for _, name := range counterNames {
			r.MetricsDelta[name] = after[name] - before[name]
			if r.MetricsDelta[name] < 0 {
				r.MetricsErrors++
			}
		}
		r.Outbox["pendingAfter"] = after["wallet_outbox_pending"]
		r.Outbox["lagAfterSeconds"] = after["wallet_outbox_oldest_age_seconds"]
		r.Outbox["maxSampledPending"] = math.Max(r.Outbox["maxSampledPending"], after["wallet_outbox_pending"])
		r.Outbox["maxSampledLagSeconds"] = math.Max(r.Outbox["maxSampledLagSeconds"], after["wallet_outbox_oldest_age_seconds"])
	}
	for i, w := range wallets {
		status, b, e := c.call(ctx, bases[i%len(bases)], "POST", "/wallets/"+w.ID+"/reconciliation", c.internal, "", nil)
		var rec store.Reconciliation
		want := fmt.Sprintf("%d.00", 1000000-expected[w.ID])
		if e == nil && status == 200 && json.Unmarshal(b, &rec) == nil && rec.Consistent && rec.Stored.Amount == want && rec.Calculated.Amount == want && rec.Difference.Amount == "0.00" && rec.Entries == int64(expected[w.ID]+1) {
			r.Reconciled++
		}
	}
	r.Integrity = r.Failures == 0 && r.IdentityErrors == 0 && r.Reconciled == cfg.Wallets && len(identities) == r.Unique && r.Replays == cfg.Requests-r.Unique && r.MetricsErrors == 0
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(output), 0750); err != nil {
		return err
	}
	if err = os.WriteFile(output, append(data, '\n'), 0600); err != nil {
		return err
	}
	fmt.Printf("requests=%d unique=%d failures=%d replays=%d rps=%.2f p95_ms=%.2f reconciled=%d/%d integrity=%t report=%s\n", cfg.Requests, r.Unique, r.Failures, r.Replays, r.Throughput, r.Latency["p95"], r.Reconciled, cfg.Wallets, r.Integrity, output)
	if !r.Integrity {
		return fmt.Errorf("benchmark checks failed; inspect report")
	}
	return nil
}
func main() {
	var cfg settings
	var output string
	flag.IntVar(&cfg.Requests, "requests", 1000, "total measured requests (1..100000)")
	flag.IntVar(&cfg.Concurrency, "concurrency", 8, "concurrent clients (1..256)")
	flag.IntVar(&cfg.Wallets, "wallets", 16, "fresh wallets (1..1000)")
	flag.IntVar(&cfg.DuplicateEvery, "duplicate-every", 10, "repeat previous operation every N requests; 0 disables")
	flag.StringVar(&output, "output", "test-results/benchmark.json", "JSON report destination")
	flag.Parse()
	if cfg.Requests < 1 || cfg.Requests > 100000 || cfg.Concurrency < 1 || cfg.Concurrency > 256 || cfg.Wallets < 1 || cfg.Wallets > 1000 || cfg.DuplicateEvery < 0 || cfg.DuplicateEvery == 1 {
		fmt.Fprintln(os.Stderr, "invalid benchmark settings")
		os.Exit(2)
	}
	if err := run(cfg, output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
