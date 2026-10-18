package domain

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func moneyDTO(t *testing.T, m Money) MoneyJSON {
	t.Helper()
	dto, err := m.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return dto
}

func TestInvalidMoneySerialization(t *testing.T) {
	if _, err := (Money{}).JSON(); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid DTO accepted", err)
	}
	if _, err := Format(0, "XXX"); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid currency accepted", err)
	}
	if _, err := json.Marshal(Money{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid JSON accepted", err)
	}
	m, err := FromMinor(math.MinInt64, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	if got := moneyDTO(t, m).Amount; got != "-92233720368547758.08" {
		t.Fatal(got)
	}
}

func TestMoneyArithmetic(t *testing.T) {
	a, _ := ParseMoney(MoneyJSON{"10.00", "BRL"})
	b, _ := ParseMoney(MoneyJSON{"12.00", "BRL"})
	diff, e := a.Subtract(b)
	if e != nil || moneyDTO(t, diff).Amount != "-2.00" {
		t.Fatal(diff, e)
	}
	sum, e := diff.Add(b)
	if e != nil || sum != a {
		t.Fatal(sum, e)
	}
	neg, e := diff.Negate()
	if e != nil || moneyDTO(t, neg).Amount != "2.00" {
		t.Fatal(neg, e)
	}
	if cmp, e := a.Compare(b); e != nil || cmp != -1 {
		t.Fatal(cmp, e)
	}
	usd, _ := Zero("USD")
	if _, e = a.Add(usd); e == nil {
		t.Fatal("mixed currencies")
	}
	if _, e = (Money{}).Add(a); e == nil {
		t.Fatal("zero value accepted")
	}
	if _, e = restoreTestWallet(Money{}, 1); e == nil {
		t.Fatal("uninitialized wallet accepted")
	}
	low := Money{math.MinInt64, "BRL"}
	high := Money{math.MaxInt64, "BRL"}
	one := Money{1, "BRL"}
	if _, e = low.Negate(); e == nil {
		t.Fatal("negation overflow")
	}
	if _, e = high.Add(one); e == nil {
		t.Fatal("addition overflow")
	}
	if _, e = low.Subtract(one); e == nil {
		t.Fatal("subtraction overflow")
	}
	encoded, e := json.Marshal(low)
	if e != nil || string(encoded) != `{"amount":"-92233720368547758.08","currency":"BRL"}` {
		t.Fatal(string(encoded), e)
	}
	w, _ := restoreTestWallet(a, 1)
	if w.apply(math.MinInt64) != "INSUFFICIENT_FUNDS" || w.Balance() != a {
		t.Fatal("extreme debit corrupted wallet")
	}
}
