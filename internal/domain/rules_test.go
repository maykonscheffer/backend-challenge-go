package domain

import (
	"errors"
	"testing"
)

func TestPublicWalletMovements(t *testing.T) {
	brl, _ := ParseMoney(MoneyJSON{"10.00", "BRL"})
	usd, _ := ParseMoney(MoneyJSON{"10.00", "USD"})
	w, _ := restoreTestWallet(brl, 1)
	before := w
	if err := w.Credit(usd); !errors.Is(err, RuleError("CURRENCY_MISMATCH")) || w != before {
		t.Fatal("currency mismatch changed wallet", err)
	}
	if err := w.Debit(Money{}); !errors.Is(err, ErrInvalid) || w != before {
		t.Fatal("invalid money accepted", err)
	}
	if err := w.Debit(brl); err != nil || w.Balance().Minor() != 0 || w.Version() != 2 {
		t.Fatal("debit", err)
	}
	var rule RuleError
	if err := w.Debit(brl); !errors.As(err, &rule) || rule != RuleError("INSUFFICIENT_FUNDS") {
		t.Fatal("unclassifiable rejection", err)
	}
	if err := w.Credit(brl); err != nil || w.Balance() != brl || w.Version() != 3 {
		t.Fatal("credit", err)
	}
}

func TestExternalOperationMatrix(t *testing.T) {
	base := Operation{ProviderID: "p", ExternalID: "op", PlayerID: "player", WalletID: "wallet", RoundID: "round", GameID: "game"}
	for _, kind := range []string{"BET", "WIN", "LOSS", "REFUND", "ROLLBACK"} {
		t.Run(kind, func(t *testing.T) {
			o := base
			o.Kind = kind
			o.Money = MoneyJSON{"0.00", "BRL"}
			if kind == "REFUND" || kind == "ROLLBACK" {
				o.Reference = "bet"
			}
			_, err := o.Validate()
			if (err == nil) != (kind == "LOSS") {
				t.Fatal("wrong zero policy", err)
			}
			o.Money.Amount = "5.00"
			m, err := o.Validate()
			if kind == "LOSS" {
				if err == nil {
					t.Fatal("positive loss accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ref := &Reference{Kind: "BET", Status: "PROCESSED", PlayerID: "player", WalletID: "wallet", Currency: "BRL", RoundID: "round", Amount: 500}
			delta, err := Decide(o, m, "player", "BRL", ref)
			want := int64(500)
			if kind == "BET" {
				want = -500
			}
			if err != nil || delta != want {
				t.Fatal(delta, err)
			}
			if o.Reference != "" {
				ref.RoundID = "other"
				if _, err = Decide(o, m, "player", "BRL", ref); !errors.Is(err, RuleError("REFERENCE_MISMATCH")) {
					t.Fatal(err)
				}
			}
		})
	}
	base.Kind = "ROLLBACK"
	base.Money = MoneyJSON{"5.00", "BRL"}
	m, _ := ParseMoney(base.Money)
	if _, err := Decide(base, m, "player", "BRL", nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing reference must reject without panic", err)
	}
}
