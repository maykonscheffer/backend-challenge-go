package bootstrap

import (
	"context"
	"errors"
	"example.com/wallet/internal/httpapi"
	"example.com/wallet/internal/messaging"
	"example.com/wallet/internal/store"
	"example.com/wallet/internal/telemetry"
	"example.com/wallet/migrations"
	"fmt"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
)

type Config struct{ Database, Issuer, JWKS, Audience, Addr string }

func config() (Config, error) {
	c := Config{os.Getenv("DATABASE_URL"), os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_JWKS_URL"), os.Getenv("OIDC_AUDIENCE"), os.Getenv("HTTP_ADDR")}
	if c.Addr == "" {
		c.Addr = ":8080"
	}
	if c.Database == "" || c.Issuer == "" || c.JWKS == "" || c.Audience == "" {
		return c, fmt.Errorf("DATABASE_URL, OIDC_ISSUER, OIDC_JWKS_URL and OIDC_AUDIENCE are required")
	}
	return c, nil
}
func pool(lc fx.Lifecycle, c Config) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(c.Database)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 10
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "8000"
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "5000"
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		if err := p.Ping(ctx); err != nil {
			p.Close()
			return err
		}
		var n int
		err := p.QueryRow(ctx, "SELECT COALESCE(MAX(version),0) FROM schema_version").Scan(&n)
		if err == nil && n != migrations.LatestVersion {
			err = fmt.Errorf("schema version %d; expected %d", n, migrations.LatestVersion)
		}
		if err != nil {
			p.Close()
		}
		return err
	}, OnStop: func(context.Context) error { p.Close(); return nil }})
	return p, nil
}
func auth(c Config) *httpapi.Auth {
	keys := oidc.NewRemoteKeySet(context.Background(), c.JWKS)
	return &httpapi.Auth{Verifier: oidc.NewVerifier(c.Issuer, keys, &oidc.Config{ClientID: c.Audience, SupportedSigningAlgs: []string{oidc.RS256}})}
}
func serve(lc fx.Lifecycle, c Config, api *httpapi.Server, shutdown fx.Shutdowner) {
	server := &http.Server{Addr: c.Addr, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan struct{})
	lc.Append(fx.Hook{OnStart: func(context.Context) error {
		listener, err := net.Listen("tcp", c.Addr)
		if err != nil {
			return err
		}
		go func() {
			defer close(done)
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("http server stopped", "error", err)
				_ = shutdown.Shutdown(fx.ExitCode(1))
			}
		}()
		return nil
	}, OnStop: func(ctx context.Context) error {
		err := server.Shutdown(ctx)
		if err != nil {
			_ = server.Close()
		}
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
		return err
	}})
}
func New(options ...fx.Option) *fx.App {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	base := []fx.Option{fx.Module("configuration", fx.Provide(config)), fx.Module("persistence", fx.Provide(telemetry.New, pool, func(p *pgxpool.Pool, tracing *telemetry.Tracing) *store.Store {
		s := store.New(p)
		s.Tracing = tracing
		return s
	})), fx.Module("messaging", fx.Provide(messaging.New, messaging.NewPublisher, messaging.NewPendingWorker)), fx.Module("http", fx.Provide(auth, httpapi.New), fx.Invoke(serve)), fx.StopTimeout(20 * time.Second)}
	return fx.New(append(base, options...)...)
}
