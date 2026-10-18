package bootstrap

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStartupRejectsOldSchema(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("requires PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "old_schema_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	if _, err = admin.Exec(ctx, "CREATE TABLE "+schema+".schema_version(version integer); INSERT INTO "+schema+".schema_version VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	var p *pgxpool.Pool
	app := fx.New(fx.Provide(func() Config { return Config{Database: u.String()} }, pool), fx.Populate(&p))
	if err = app.Start(ctx); err == nil || !strings.Contains(err.Error(), "expected 9") {
		t.Fatal("old schema accepted", err)
	}
	if p != nil && p.Stat().TotalConns() != 0 {
		t.Fatal("failed startup leaked pool")
	}
}

func TestLifecycleWithRealDependencies(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("requires Docker integration environment")
	}
	t.Setenv("DATABASE_URL", os.Getenv("TEST_DATABASE_URL"))
	t.Setenv("SQS_ENDPOINT", os.Getenv("TEST_SQS_ENDPOINT"))
	t.Setenv("SQS_ALLOWED_PROVIDERS", "provider-a,provider-b")
	t.Setenv("OIDC_ISSUER", "http://localhost:8180/realms/wallet")
	t.Setenv("OIDC_JWKS_URL", "http://keycloak:8080/realms/wallet/protocol/openid-connect/certs")
	t.Setenv("OIDC_AUDIENCE", "wallet-api")
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	var pool *pgxpool.Pool
	app := New(fx.Populate(&pool))
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	stop, done := context.WithTimeout(context.Background(), 20*time.Second)
	defer done()
	if err := app.Stop(stop); err != nil {
		t.Fatal(err)
	}
	if pool.Stat().TotalConns() != 0 {
		t.Fatal("pool not closed after worker shutdown")
	}
}
