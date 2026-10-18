package test

import (
	"context"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestAuditTimestamps(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	w := s.wallet(t, "100.00")
	var initial, after time.Time
	if err := s.db.QueryRow(ctx, "SELECT updated_at FROM wallets WHERE id=$1", w.ID).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	loss := s.submit(t, op(w, "LOSS", "0.00"), uuid.NewString(), 200)
	if err := s.db.QueryRow(ctx, "SELECT updated_at FROM wallets WHERE id=$1", w.ID).Scan(&after); err != nil || !after.Equal(initial) {
		t.Fatal("LOSS changed wallet timestamp", err)
	}
	var ordered bool
	if err := s.db.QueryRow(ctx, "SELECT completed_at IS NOT NULL AND completed_at>=created_at AND updated_at=completed_at FROM transactions WHERE id=$1", loss.TransactionID).Scan(&ordered); err != nil || !ordered {
		t.Fatal("invalid terminal timestamps", err)
	}
	bet := op(w, "BET", "10.00")
	refund := op(w, "REFUND", "10.00")
	refund.Reference = bet.ExternalID
	key := uuid.NewString()
	pending := s.submit(t, refund, key, 202)
	if err := s.db.QueryRow(ctx, "SELECT completed_at IS NULL AND updated_at>=created_at FROM transactions WHERE id=$1", pending.TransactionID).Scan(&ordered); err != nil || !ordered {
		t.Fatal("pending falsely marked complete", err)
	}
	s.submit(t, bet, uuid.NewString(), 200)
	final := awaitTerminal(t, s, pending.TransactionID)
	if final.Status != "PROCESSED" {
		t.Fatal(final)
	}
	var completed, updated time.Time
	if err := s.db.QueryRow(ctx, "SELECT completed_at,updated_at FROM transactions WHERE id=$1", pending.TransactionID).Scan(&completed, &updated); err != nil || !completed.Equal(updated) {
		t.Fatal(err)
	}
	s.submit(t, refund, key, 200)
	var replayCompleted time.Time
	if err := s.db.QueryRow(ctx, "SELECT completed_at FROM transactions WHERE id=$1", pending.TransactionID).Scan(&replayCompleted); err != nil || !completed.Equal(replayCompleted) {
		t.Fatal("replay changed completion timestamp", err)
	}
	if _, err := s.db.Exec(ctx, "UPDATE transactions SET completed_at=now() WHERE id=$1", pending.TransactionID); err == nil {
		t.Fatal("terminal audit timestamp changed")
	}
	if err := s.db.QueryRow(ctx, "SELECT updated_at FROM wallets WHERE id=$1", w.ID).Scan(&after); err != nil || !after.After(initial) {
		t.Fatal("financial movement did not update timestamp", err)
	}
	s.check(t, w, "100.00", 3)
}
