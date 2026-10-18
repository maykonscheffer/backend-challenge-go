package test

import (
	"context"
	"encoding/json"
	"example.com/wallet/internal/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func awaitTerminal(t *testing.T, s *suite, id string) store.Result {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		r, err := store.New(s.db).Transaction(context.Background(), "provider-a", id, "")
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != "PENDING_REFERENCE" {
			return r
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("pending reference did not resolve")
	return store.Result{}
}

func TestPendingReferences(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	t.Run("refund before bet, duplicate replay and competing reversals", func(t *testing.T) {
		w := s.wallet(t, "100.00")
		bet := op(w, "BET", "10.00")
		refund := op(w, "REFUND", "10.00")
		refund.Reference = bet.ExternalID
		refund.RoundID = bet.RoundID
		key := uuid.NewString()
		pending := s.submit(t, refund, key, 202)
		if pending.Status != "PENDING_REFERENCE" {
			t.Fatal(pending)
		}
		replay := s.submit(t, refund, key, 202)
		if replay.TransactionID != pending.TransactionID || !replay.Replay {
			t.Fatal(replay)
		}
		other := refund
		other.ExternalID = uuid.NewString()
		second := s.submit(t, other, uuid.NewString(), 202)
		conflict := refund
		conflict.Money.Amount = "11.00"
		s.submit(t, conflict, key, 409)
		s.check(t, w, "100.00", 1)
		var beforeVersion int64
		if err := s.db.QueryRow(ctx, "SELECT version FROM wallets WHERE id=$1", w.ID).Scan(&beforeVersion); err != nil || beforeVersion != 1 {
			t.Fatal("pending changed wallet version", err)
		}
		s.submit(t, bet, uuid.NewString(), 200)
		a, b := awaitTerminal(t, s, pending.TransactionID), awaitTerminal(t, s, second.TransactionID)
		if !((a.Status == "PROCESSED" && b.Code == "ALREADY_REVERSED") || (b.Status == "PROCESSED" && a.Code == "ALREADY_REVERSED")) {
			t.Fatalf("bad reversal results: %+v %+v", a, b)
		}
		terminal := s.submit(t, refund, key, map[bool]int{true: 200, false: 422}[a.Status == "PROCESSED"])
		if terminal.TransactionID != pending.TransactionID || !terminal.Replay {
			t.Fatal(terminal)
		}
		s.check(t, w, "100.00", 3)
		if _, err := s.db.Exec(ctx, "UPDATE transactions SET status='PENDING_REFERENCE' WHERE id=$1", a.TransactionID); err == nil {
			t.Fatal("terminal transaction was mutable")
		}
	})
	t.Run("missing reference retries without financial effects", func(t *testing.T) {
		w := s.wallet(t, "20.00")
		o := op(w, "REFUND", "5.00")
		o.Reference = uuid.NewString()
		r := s.submit(t, o, uuid.NewString(), 202)
		deadline := time.Now().Add(6 * time.Second)
		attempts := 0
		for time.Now().Before(deadline) {
			if err := s.db.QueryRow(ctx, "SELECT reference_attempts FROM transactions WHERE id=$1", r.TransactionID).Scan(&attempts); err != nil {
				t.Fatal(err)
			}
			if attempts > 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if attempts == 0 {
			t.Fatal("worker did not retry")
		}
		s.check(t, w, "20.00", 1)
	})
	t.Run("expired durable pending rejects with event and no movement", func(t *testing.T) {
		w := s.wallet(t, "20.00")
		o := op(w, "REFUND", "5.00")
		o.Reference = uuid.NewString()
		// Seed already expired work instead of waiting 24h or weakening the immutable TTL.
		id := uuid.NewString()
		r := store.Result{TransactionID: id, Status: "PENDING_REFERENCE", Balance: w.Balance, Code: "REFERENCE_PENDING"}
		input, _ := json.Marshal(o)
		result, _ := json.Marshal(r)
		_, err := s.db.Exec(ctx, `INSERT INTO transactions(id,provider_id,external_id,idempotency_key,fingerprint,wallet_id,player_id,currency,kind,amount,round_id,game_id,status,result,input,reference_expires_at,reference_next_attempt_at)
   VALUES($1,$2,$3,$4,$5,$6,$7,'BRL','REFUND',500,$8,$9,'PENDING_REFERENCE',$10,$11,now()-interval '1 second',now())`, id, o.ProviderID, o.ExternalID, uuid.NewString(), o.Fingerprint(), o.WalletID, o.PlayerID, o.RoundID, o.GameID, result, input)
		if err != nil {
			t.Fatal(err)
		}
		got := awaitTerminal(t, s, id)
		if got.Status != "REJECTED" || got.Code != "REFERENCE_NOT_FOUND" {
			t.Fatal(got)
		}
		var n int
		if err = s.db.QueryRow(ctx, "SELECT count(*) FROM outbox WHERE transaction_id=$1 AND event_type='WagerTransactionRejected'", id).Scan(&n); err != nil || n != 1 {
			t.Fatal("missing rejection event", err)
		}
		s.check(t, w, "20.00", 1)
	})
}
