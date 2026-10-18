package domain

import (
	"math"
	"testing"
	"time"
)

func TestMoneyExactAndBoundaries(t *testing.T) {
	for _, a := range []string{"0.00", "0.01", "100.00", "92233720368547758.07"} {
		m, e := ParseMoney(MoneyJSON{a, "BRL"})
		if e != nil || moneyDTO(t, m).Amount != a {
			t.Fatalf("roundtrip %s: %v", a, e)
		}
	}
	for _, a := range []string{"1", "01.00", "1.0", "1.001", "-1.00", "1e2", "NaN", "92233720368547758.08"} {
		if _, e := ParseMoney(MoneyJSON{a, "BRL"}); e == nil {
			t.Fatalf("accepted %q", a)
		}
	}
	m, _ := ParseMoney(MoneyJSON{"92233720368547758.07", "BRL"})
	w, _ := restoreTestWallet(m, 1)
	if w.apply(1) != "BALANCE_OVERFLOW" || w.Balance().Minor() != math.MaxInt64 {
		t.Fatal("overflow changed wallet")
	}
	m, _ = ParseMoney(MoneyJSON{"100.00", "BRL"})
	w, _ = restoreTestWallet(m, 1)
	if w.apply(-8000) != "" || w.apply(-8000) != "INSUFFICIENT_FUNDS" || w.Balance().Minor() != 2000 {
		t.Fatal("negative balance")
	}
}
func TestReferenceRules(t *testing.T) {
	o := Operation{ProviderID: "p", ExternalID: "r", PlayerID: "player", WalletID: "wallet", RoundID: "round", GameID: "game", Kind: "REFUND", Money: MoneyJSON{"5.00", "BRL"}, Reference: "bet"}
	m, e := o.Validate()
	if e != nil {
		t.Fatal(e)
	}
	ref := Reference{Kind: "BET", Status: "PROCESSED", PlayerID: "player", WalletID: "wallet", Currency: "BRL", RoundID: "round", Amount: 500}
	if d, c := Decide(o, m, "player", "BRL", &ref); d != 500 || FailureCode(c) != "" {
		t.Fatal(d, c)
	}
	ref.Reversed = true
	if _, c := Decide(o, m, "player", "BRL", &ref); FailureCode(c) != "ALREADY_REVERSED" {
		t.Fatal(c)
	}
	if _, c := Decide(o, m, "player", "BRL", nil); FailureCode(c) != "REFERENCE_NOT_FOUND" {
		t.Fatal(c)
	}
	o.Kind = "OPENING"
	if _, e = o.Validate(); e == nil {
		t.Fatal("external opening accepted")
	}
}

func restoreTestWallet(m Money, v int64) (Wallet, error) {
	at := time.Now().UTC()
	return RestoreWallet("wallet", "player", m, v, at, at)
}
