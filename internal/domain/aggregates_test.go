package domain

import (
	"testing"
	"time"
)

func TestWalletIdentityAndRehydration(t *testing.T) {
	m, _ := ParseMoney(MoneyJSON{"10.00", "BRL"})
	at := time.Now().Add(-time.Hour).UTC()
	w, err := NewWallet("wallet", "player", m, at)
	if err != nil || w.ID() != "wallet" || w.PlayerID() != "player" || w.Version() != 1 {
		t.Fatal(w, err)
	}
	restored, err := RestoreWallet(w.ID(), w.PlayerID(), w.Balance(), w.Version(), w.CreatedAt(), w.UpdatedAt())
	if err != nil || restored != w {
		t.Fatal("rehydration changed state", err)
	}
	if code := w.apply(0); code != "" || w.UpdatedAt() != at {
		t.Fatal("zero movement changed timestamp")
	}
	if code := w.apply(-100); code != "" || !w.UpdatedAt().After(at) || w.Version() != 2 {
		t.Fatal("movement did not update state")
	}
	if _, err = NewWallet("", "player", m, at); err == nil {
		t.Fatal("missing identity accepted")
	}
	if _, err = RestoreWallet("w", "p", m, 1, at, at.Add(-time.Second)); err == nil {
		t.Fatal("invalid timestamps accepted")
	}
}

func TestImmutableLedgerConstruction(t *testing.T) {
	before, _ := ParseMoney(MoneyJSON{"10.00", "BRL"})
	money, _ := ParseMoney(MoneyJSON{"2.00", "BRL"})
	after, _ := ParseMoney(MoneyJSON{"8.00", "BRL"})
	at := time.Now().UTC()
	e, err := NewLedgerEntry("entry", "transaction", "wallet", "DEBIT", money, before, after, at)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreLedgerEntry(e.ID(), e.TransactionID(), e.WalletID(), e.Direction(), e.Money(), e.BalanceBefore(), e.BalanceAfter(), e.CreatedAt())
	if err != nil || restored != e {
		t.Fatal("rehydration changed ledger", err)
	}
	for _, direction := range []string{"CREDIT", "INVALID"} {
		if _, err = NewLedgerEntry("e", "t", "w", direction, money, before, after, at); err == nil {
			t.Fatal("bad direction/equation accepted")
		}
	}
	zero, _ := Zero("BRL")
	usd, _ := ParseMoney(MoneyJSON{"2.00", "USD"})
	for _, invalid := range []Money{Money{}, zero, usd} {
		if _, err = NewLedgerEntry("e", "t", "w", "DEBIT", invalid, before, after, at); err == nil {
			t.Fatal("invalid movement accepted")
		}
	}
}
