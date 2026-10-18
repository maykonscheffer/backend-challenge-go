package domain

import (
	"errors"
	"testing"
	"time"
)

func newTestTransaction(t *testing.T, o Operation) (WagerTransaction, error) {
	t.Helper()
	tr, err := NewWagerTransaction("id", o, "key", time.Now().UTC())
	if err != nil {
		return tr, err
	}
	balance, _ := Zero("BRL")
	err = tr.SetResult(balance, "")
	return tr, err
}
func TestTransactionTransitions(t *testing.T) {
	o := Operation{ProviderID: "p", ExternalID: "r", PlayerID: "player", WalletID: "wallet", RoundID: "round", GameID: "game", Kind: "REFUND", Money: MoneyJSON{"5.00", "BRL"}, Reference: "bet"}
	tr, err := newTestTransaction(t, o)
	if err != nil || tr.Status() != Pending {
		t.Fatal(tr, err)
	}
	if err = tr.WaitForReference(); err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreWagerTransaction(tr.ID(), tr.Operation(), tr.Status(), tr.FailureCode(), tr.Metadata())
	if err != nil || restored.Status() != PendingReference {
		t.Fatal(restored, err)
	}
	if err = restored.Process(); err != nil {
		t.Fatal(err)
	}
	if err = restored.Reject("LATE"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("terminal changed", err)
	}
	for _, terminal := range []TransactionStatus{Rejected, Failed} {
		tr, _ = newTestTransaction(t, o)
		if terminal == Rejected {
			err = tr.Reject("REFERENCE_NOT_FOUND")
		} else {
			err = tr.Fail("PERMANENT_STORAGE_ERROR")
		}
		if err != nil || tr.Status() != terminal {
			t.Fatal(tr, err)
		}
		if err = tr.Process(); !errors.Is(err, ErrInvalidTransition) {
			t.Fatal(err)
		}
	}
	if _, err = RestoreWagerTransaction("id", o, Processed, "ERROR", tr.Metadata()); err == nil {
		t.Fatal("inconsistent state restored")
	}
	if err = (&WagerTransaction{}).Process(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("uninitialized state accepted")
	}
}

func TestTransactionMetadataAndInternalOpening(t *testing.T) {
	m, _ := ParseMoney(MoneyJSON{"25.00", "BRL"})
	at := time.Now().UTC()
	tr, err := NewOpeningTransaction("opening", "wallet", "player", m, at)
	if err != nil {
		t.Fatal(err)
	}
	if err = tr.Process(); err == nil {
		t.Fatal("terminal without financial result")
	}
	zero, _ := Zero("BRL")
	if err = tr.SetResult(zero, ""); err == nil {
		t.Fatal("wrong opening balance")
	}
	if err = tr.SetResult(m, ""); err != nil {
		t.Fatal(err)
	}
	if err = tr.Process(); err != nil {
		t.Fatal(err)
	}
	o := tr.Operation()
	meta := tr.Metadata()
	if o.ProviderID != "" || o.ExternalID != "" || o.RoundID != "" || o.GameID != "" || meta.IdempotencyKey != "" || meta.PayloadHash != "" || meta.CompletedAt.IsZero() || tr.Money() != m {
		t.Fatal("invalid internal metadata", tr)
	}
	restored, err := RestoreWagerTransaction(tr.ID(), o, tr.Status(), tr.FailureCode(), meta)
	if err != nil || restored != tr {
		t.Fatal("rehydration changed record", err)
	}
	bad := meta
	bad.Balance = zero
	if _, err = RestoreWagerTransaction(tr.ID(), o, tr.Status(), tr.FailureCode(), bad); !errors.Is(err, ErrInvalid) {
		t.Fatal("inconsistent opening balance restored", err)
	}
	if err = restored.SetResult(m, ""); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("terminal result mutable", err)
	}
	if _, err = NewOpeningTransaction("id", "wallet", "player", zero, at); err == nil {
		t.Fatal("zero opening accepted")
	}
	if _, err = o.Validate(); err == nil {
		t.Fatal("external opening accepted")
	}
	o.Kind = "BET"
	o.ProviderID = "provider"
	o.ExternalID = "external"
	o.RoundID = "round"
	o.GameID = "game"
	external, err := NewWagerTransaction("id", o, "custom-key", at)
	if err != nil {
		t.Fatal(err)
	}
	meta = external.Metadata()
	if meta.IdempotencyKey != "custom-key" || meta.PayloadHash != o.Fingerprint() || meta.CreatedAt != at || external.Money() != m {
		t.Fatal("lost external metadata")
	}
	meta.PayloadHash = "tampered"
	if _, err = RestoreWagerTransaction(external.ID(), o, Pending, "", meta); err == nil {
		t.Fatal("tampered fingerprint accepted")
	}
}
