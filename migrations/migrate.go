package migrations

import (
	"context"
	"embed"
	"fmt"
	"github.com/jackc/pgx/v5"
)

//go:embed *.sql
var files embed.FS

const LatestVersion = 9

func Run(ctx context.Context, url, direction string) error {
	if direction != "up" && direction != "down" {
		return fmt.Errorf("migration direction must be up or down")
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(827451)"); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT to_regclass('public.schema_version') IS NOT NULL").Scan(&exists); err != nil {
		return err
	}
	version := 0
	if exists {
		if err = tx.QueryRow(ctx, "SELECT COALESCE(MAX(version),0) FROM schema_version").Scan(&version); err != nil {
			return err
		}
	}
	if direction == "up" {
		for n := version + 1; n <= LatestVersion; n++ {
			b, e := files.ReadFile(fmt.Sprintf("%03d_up.sql", n))
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, string(b)); e != nil {
				return e
			}
		}
	} else {
		for n := version; n >= 1; n-- {
			b, e := files.ReadFile(fmt.Sprintf("%03d_down.sql", n))
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, string(b)); e != nil {
				return e
			}
		}
	}
	return tx.Commit(ctx)
}
