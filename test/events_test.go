package test

import (
	"context"
	"encoding/json"
	"example.com/wallet/internal/domain"
	"github.com/google/uuid"
	"testing"
)

func TestEventContracts(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	w := s.wallet(t, "100.00")
	bet := op(w, "BET", "10.00")
	r := s.submit(t, bet, uuid.NewString(), 200)
	var raw []byte
	if err := s.db.QueryRow(ctx, "SELECT payload FROM outbox WHERE transaction_id=$1 AND event_type='WalletBalanceChanged'", r.TransactionID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var e domain.Event[domain.BalanceChangedData]
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	if e.CorrelationID == "" || e.EventID == "" || e.Version != 1 || e.Data.Direction != "DEBIT" || e.Data.BalanceBefore.Amount != "100.00" || e.Data.BalanceAfter.Amount != "90.00" || e.Data.Money.Amount != "10.00" || e.Data.WalletVersion != 2 {
		t.Fatalf("invalid balance event: %+v", e)
	}
	if _, err := s.db.Exec(ctx, "UPDATE outbox SET payload='{}' WHERE event_id=$1", e.EventID); err == nil {
		t.Fatal("snapshot was mutable")
	}
	var opening []byte
	if err := s.db.QueryRow(ctx, "SELECT payload FROM outbox WHERE payload->>'aggregateId'=$1 AND event_type='WalletBalanceChanged' ORDER BY seq LIMIT 1", w.ID).Scan(&opening); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(opening, &e); err != nil {
		t.Fatal(err)
	}
	if e.Data.WalletVersion != 1 || e.Data.BalanceBefore.Amount != "0.00" || e.Data.Direction != "CREDIT" {
		t.Fatal(e)
	}
	refund := op(w, "REFUND", "10.00")
	refund.Reference = uuid.NewString()
	p := s.submit(t, refund, uuid.NewString(), 202)
	if err := s.db.QueryRow(ctx, "SELECT payload FROM outbox WHERE transaction_id=$1 AND event_type='WagerTransactionPendingReference'", p.TransactionID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var pending domain.Event[domain.TransactionEventData]
	if err := json.Unmarshal(raw, &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Data.Status != "PENDING_REFERENCE" || pending.Data.ProviderID != "provider-a" || pending.Data.Money.Amount != "10.00" || pending.CorrelationID == "" {
		t.Fatal(pending)
	}
	s.submit(t, refund, uuid.NewString(), 409)
}
