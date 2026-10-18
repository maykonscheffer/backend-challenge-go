package store

import (
	"context"
	"encoding/json"
	"errors"
	"example.com/wallet/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
)

// Only PostgreSQL data exceptions are considered permanent for financial
// processing. Network, timeout, serialization, constraints and configuration errors are
// not classified here: exhausting retries alone never proves permanence.
func permanentDataError(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && strings.HasPrefix(pg.Code, "22")
}

// Called only after the failed financial attempt has rolled back. The separate
// transaction locks the same wallet and rechecks terminal state, so another worker
// that completed in the meantime wins; its result cannot be overwritten.
func (s *Store) failPending(ctx context.Context, o domain.Operation, key string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var balance int64
	var currency string
	if err = tx.QueryRow(ctx, "SELECT balance,currency FROM wallets WHERE id=$1 FOR UPDATE", o.WalletID).Scan(&balance, &currency); err != nil {
		return err
	}
	var body []byte
	var hash string
	if err = tx.QueryRow(ctx, "SELECT result,fingerprint FROM transactions WHERE provider_id=$1 AND idempotency_key=$2", o.ProviderID, key).Scan(&body, &hash); err != nil {
		return err
	}
	var r Result
	if err = json.Unmarshal(body, &r); err != nil {
		return err
	}
	if hash != o.Fingerprint() {
		return ErrConflict
	}
	if r.Status != "PENDING_REFERENCE" {
		return tx.Commit(ctx)
	}
	entity, err := restoreTransaction(ctx, tx, o, key, r)
	if err != nil {
		return err
	}
	current, err := domain.FromMinor(balance, currency)
	if err != nil {
		return err
	}
	if err = entity.SetResult(current, entity.Metadata().ReferenceID); err != nil {
		return err
	}
	if err = entity.Fail("PERMANENT_DATA_ERROR"); err != nil {
		return err
	}
	r.Status = string(entity.Status())
	r.Code = entity.FailureCode()
	r.Balance, err = domain.Format(balance, currency)
	if err != nil {
		return err
	}
	r.Replay = false
	body, err = json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE transactions SET status=$2,result=$3 WHERE id=$1", r.TransactionID, r.Status, body); err != nil {
		return err
	}
	if err = event(ctx, tx, r, o.WalletID, "WagerTransactionFailed"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
