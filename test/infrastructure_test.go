package test

import (
	"context"
	"example.com/wallet/internal/domain"
	"example.com/wallet/internal/httpapi"
	"example.com/wallet/internal/store"
	"example.com/wallet/migrations"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestExpiredIdentityToken(t *testing.T) {
	s := setup(t)
	keys := oidc.NewRemoteKeySet(context.Background(), strings.TrimSuffix(os.Getenv("TEST_TOKEN_URL"), "/token")+"/certs")
	// Verify a genuinely signed IdP token at a clock after its lifetime; no forged JWT.
	verifier := oidc.NewVerifier("http://localhost:8180/realms/wallet", keys, &oidc.Config{ClientID: "wallet-api", SupportedSigningAlgs: []string{oidc.RS256}, Now: func() time.Time { return time.Now().Add(48 * time.Hour) }})
	api := &httpapi.Server{Store: store.New(s.db), Auth: &httpapi.Auth{Verifier: verifier}}
	r := httptest.NewRequest("GET", "/wallets/"+uuid.NewString(), nil)
	r.Header.Set("Authorization", "Bearer "+s.internal)
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, r)
	if response.Code != 401 {
		t.Fatalf("expired IdP token accepted: %d", response.Code)
	}
}

func TestMigrationsFreshDatabase(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("requires PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	name := "migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP DATABASE "+quoted)
	cfg, err := pgx.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	// Use parsed URL only to replace the known database segment, retaining credentials privately.
	url := strings.Replace(os.Getenv("TEST_DATABASE_URL"), "/"+cfg.Database+"?", "/"+name+"?", 1)
	for _, direction := range []string{"up", "up", "down", "up"} {
		if err = migrations.Run(ctx, url, direction); err != nil {
			t.Fatalf("migration %s: %v", direction, err)
		}
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var version int
	if err = conn.QueryRow(ctx, "SELECT max(version) FROM schema_version").Scan(&version); err != nil || version != 9 {
		t.Fatal(version, err)
	}
}

func TestMetricsContract(t *testing.T) {
	s := setup(t)
	status, body, err := s.call(s.urls[0], "GET", "/metrics", s.internal, "", nil)
	if err != nil || status != 200 {
		t.Fatalf("metrics: %d %v", status, err)
	}
	for _, name := range []string{"wallet_transactions{status=", "wallet_outbox_pending", "wallet_outbox_oldest_age_seconds", "wallet_http_duration_seconds_sum", "wallet_http_conflicts_total", "wallet_outbox_retries_total", "wallet_sql_concurrency_conflicts_total", "wallet_sql_retries_total", "wallet_sqs_replays_total", "wallet_sqs_duration_seconds_sum", "wallet_sqs_duration_seconds_count"} {
		if !strings.Contains(string(body), name) {
			t.Fatal("missing metric", name)
		}
	}
}

func TestSQLLockConflictMetric(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	w := s.wallet(t, "20.00")
	lock, err := s.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err = lock.Exec(ctx, "SELECT id FROM wallets WHERE id=$1 FOR UPDATE", w.ID); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "100ms"
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	repo := store.New(p)
	_, err = repo.Process(ctx, op(w, "BET", "5.00"), uuid.NewString())
	if err == nil || repo.ConcurrencyConflicts.Load() != 1 {
		t.Fatal("lock conflict not observed", err, repo.ConcurrencyConflicts.Load())
	}
	if err = lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	s.check(t, w, "20.00", 1)
	zero, err := domain.Zero("BRL")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Open(ctx, uuid.NewString(), zero); err != nil {
		t.Fatal("pool did not recover", err)
	}
}
