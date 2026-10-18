package store

import (
	"context"
	"example.com/wallet/internal/domain"
	"github.com/jackc/pgx/v5"
	"time"
)

// Read the original audit fields while the caller holds the wallet lock.
func restoreTransaction(ctx context.Context, tx pgx.Tx, o domain.Operation, key string, r Result) (domain.WagerTransaction, error) {
	meta := domain.TransactionMetadata{IdempotencyKey: key, HasResult: true}
	var completed *time.Time
	err := tx.QueryRow(ctx, `SELECT fingerprint,created_at,updated_at,completed_at,COALESCE(reference_id::text,'') FROM transactions WHERE id=$1`, r.TransactionID).Scan(&meta.PayloadHash, &meta.CreatedAt, &meta.UpdatedAt, &completed, &meta.ReferenceID)
	if err != nil {
		return domain.WagerTransaction{}, err
	}
	if completed != nil {
		meta.CompletedAt = *completed
	}
	meta.Balance, err = domain.ParseMoney(r.Balance)
	if err != nil {
		return domain.WagerTransaction{}, err
	}
	return domain.RestoreWagerTransaction(r.TransactionID, o, domain.TransactionStatus(r.Status), r.Code, meta)
}
