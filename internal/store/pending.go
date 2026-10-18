package store

import (
	"context"
	"encoding/json"
	"example.com/wallet/internal/domain"
)

func operationJSON(o domain.Operation) []byte { b, _ := json.Marshal(o); return b }

// ResumePending discovers durable work without holding a transaction across wallets.
// Each operation then takes its wallet lock and rechecks status and due time.
// Overlapping workers may discover the same row; only one can perform its transition.
func (s *Store) ResumePending(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT input,idempotency_key FROM transactions
 WHERE status='PENDING_REFERENCE' AND reference_next_attempt_at<=now()
 ORDER BY reference_next_attempt_at,id LIMIT 16`)
	if err != nil {
		return err
	}
	type work struct {
		o   domain.Operation
		key string
	}
	batch := []work{}
	for rows.Next() {
		var b []byte
		var w work
		if err = rows.Scan(&b, &w.key); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(b, &w.o); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, w)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var first error
	for _, w := range batch {
		m, e := w.o.Validate()
		if e == nil {
			_, e = s.process(ctx, w.o, w.key, m, "", "", true)
		}
		if permanentDataError(e) {
			e = s.failPending(ctx, w.o, w.key)
		}
		if e != nil && first == nil {
			first = e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return first
}
