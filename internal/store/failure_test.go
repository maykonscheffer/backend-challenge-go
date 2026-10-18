package store

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"testing"
)

func TestPermanentDataClassification(t *testing.T) {
	for _, code := range []string{"22000", "22003", "22001"} {
		if !permanentDataError(fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: code})) {
			t.Fatal(code)
		}
	}
	for _, code := range []string{"23505", "23514", "40001", "40P01", "55P03", "57014", "08006", "42501", "42P01"} {
		if permanentDataError(&pgconn.PgError{Code: code}) {
			t.Fatal("transient or operational error made terminal", code)
		}
	}
	for _, err := range []error{nil, context.Canceled, context.DeadlineExceeded, ErrUnavailable} {
		if permanentDataError(err) {
			t.Fatal(err)
		}
	}
}
