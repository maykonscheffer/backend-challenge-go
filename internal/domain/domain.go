// Package domain contains financial rules without transport or database dependencies.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrInvalid = errors.New("invalid input")
var amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.[0-9]{2}$`)

type Money struct {
	minor    int64
	currency string
}
type MoneyJSON struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func ParseMoney(v MoneyJSON) (Money, error) {
	if v.Currency != "BRL" && v.Currency != "USD" && v.Currency != "EUR" {
		return Money{}, fmt.Errorf("%w: currency must be BRL, USD or EUR", ErrInvalid)
	}
	if !amountPattern.MatchString(v.Amount) {
		return Money{}, fmt.Errorf("%w: amount must be a nonnegative decimal string with two places", ErrInvalid)
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(v.Amount, ".", ""), 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("%w: amount overflow", ErrInvalid)
	}
	return Money{n, v.Currency}, nil
}
func (m Money) Minor() int64     { return m.minor }
func (m Money) Currency() string { return m.currency }
func Format(n int64, c string) (MoneyJSON, error) {
	if !(Money{currency: c}).valid() {
		return MoneyJSON{}, ErrInvalid
	}
	sign := ""
	u := uint64(n)
	if n < 0 {
		sign = "-"
		u = uint64(-(n + 1)) + 1
	}
	return MoneyJSON{fmt.Sprintf("%s%d.%02d", sign, u/100, u%100), c}, nil
}
func (m Money) JSON() (MoneyJSON, error) { return Format(m.minor, m.currency) }

type Wallet struct {
	id, playerID         string
	createdAt, updatedAt time.Time
	balance              Money
	version              int64
}

func NewWallet(id, playerID string, balance Money, at time.Time) (Wallet, error) {
	return RestoreWallet(id, playerID, balance, 1, at, at)
}
func RestoreWallet(id, playerID string, balance Money, version int64, createdAt, updatedAt time.Time) (Wallet, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(playerID) == "" || createdAt.IsZero() || updatedAt.Before(createdAt) || !balance.valid() || balance.minor < 0 || version < 1 {
		return Wallet{}, ErrInvalid
	}
	return Wallet{id: id, playerID: playerID, createdAt: createdAt.UTC(), updatedAt: updatedAt.UTC(), balance: balance, version: version}, nil
}
func (w Wallet) ID() string           { return w.id }
func (w Wallet) PlayerID() string     { return w.playerID }
func (w Wallet) CreatedAt() time.Time { return w.createdAt }
func (w Wallet) UpdatedAt() time.Time { return w.updatedAt }
func (w Wallet) Balance() Money       { return w.balance }
func (w Wallet) Version() int64       { return w.version }
func (w *Wallet) apply(delta int64) string {
	if w.id == "" || w.playerID == "" || w.createdAt.IsZero() || !w.balance.valid() || w.version < 1 || w.balance.minor < 0 {
		return "INVALID_WALLET"
	}
	if delta == math.MinInt64 {
		return "INSUFFICIENT_FUNDS"
	}
	if delta < 0 && w.balance.minor < -delta {
		return "INSUFFICIENT_FUNDS"
	}
	if delta > 0 && w.balance.minor > math.MaxInt64-delta {
		return "BALANCE_OVERFLOW"
	}
	if delta != 0 {
		if w.version == math.MaxInt64 {
			return "VERSION_OVERFLOW"
		}
		w.balance.minor += delta
		w.version++
		at := time.Now().UTC()
		if at.After(w.updatedAt) {
			w.updatedAt = at
		}
	}
	return ""
}

type Operation struct {
	ProviderID string    `json:"providerId"`
	ExternalID string    `json:"externalTransactionId"`
	PlayerID   string    `json:"playerId"`
	WalletID   string    `json:"walletId"`
	RoundID    string    `json:"roundId"`
	GameID     string    `json:"gameId"`
	Kind       string    `json:"kind"`
	Money      MoneyJSON `json:"money"`
	Reference  string    `json:"referenceExternalTransactionId,omitempty"`
}

func (o Operation) Validate() (Money, error) {
	for _, s := range []string{o.ProviderID, o.ExternalID, o.PlayerID, o.WalletID, o.RoundID, o.GameID} {
		if strings.TrimSpace(s) == "" || len(s) > 200 {
			return Money{}, ErrInvalid
		}
	}
	m, err := ParseMoney(o.Money)
	if err != nil {
		return Money{}, err
	}
	switch o.Kind {
	case "LOSS":
		if m.minor != 0 || o.Reference != "" {
			return Money{}, ErrInvalid
		}
	case "BET":
		if m.minor == 0 || o.Reference != "" {
			return Money{}, ErrInvalid
		}
	case "WIN":
		if m.minor == 0 {
			return Money{}, ErrInvalid
		}
	case "REFUND", "ROLLBACK":
		if m.minor == 0 || o.Reference == "" {
			return Money{}, ErrInvalid
		}
	default:
		return Money{}, ErrInvalid
	}
	if len(o.Reference) > 200 || (o.Reference != "" && o.Reference == o.ExternalID) {
		return Money{}, ErrInvalid
	}
	return m, nil
}
func (o Operation) Fingerprint() string {
	// encoding/json sorts map keys, recursively; all monetary fields remain strings.
	v := map[string]any{"providerId": o.ProviderID, "externalTransactionId": o.ExternalID, "playerId": o.PlayerID, "walletId": o.WalletID, "roundId": o.RoundID, "gameId": o.GameID, "kind": o.Kind, "money": map[string]string{"amount": o.Money.Amount, "currency": o.Money.Currency}}
	if o.Reference != "" {
		v["referenceExternalTransactionId"] = o.Reference
	}
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type Reference struct {
	Kind, Status, PlayerID, WalletID, Currency, RoundID string
	Amount                                              int64
	Reversed                                            bool
}

func decide(o Operation, m Money, player, currency string, ref *Reference) (int64, string) {
	if o.PlayerID != player {
		return 0, "PLAYER_MISMATCH"
	}
	if m.currency != currency {
		return 0, "CURRENCY_MISMATCH"
	}
	if o.Reference != "" {
		if ref == nil {
			return 0, "REFERENCE_NOT_FOUND"
		}
		if ref.Status != "PROCESSED" {
			return 0, "REFERENCE_NOT_PROCESSED"
		}
		if ref.PlayerID != o.PlayerID || ref.WalletID != o.WalletID || ref.Currency != currency || ref.RoundID != o.RoundID {
			return 0, "REFERENCE_MISMATCH"
		}
		if o.Kind == "WIN" && ref.Kind != "BET" {
			return 0, "REFERENCE_KIND_INVALID"
		}
		if o.Kind == "REFUND" || o.Kind == "ROLLBACK" {
			if (o.Kind == "REFUND" && ref.Kind != "BET") || (o.Kind == "ROLLBACK" && ref.Kind != "BET" && ref.Kind != "WIN" && ref.Kind != "REFUND") {
				return 0, "REFERENCE_KIND_INVALID"
			}
			if m.minor != ref.Amount {
				return 0, "REVERSAL_AMOUNT_MISMATCH"
			}
			if ref.Reversed {
				return 0, "ALREADY_REVERSED"
			}
		}
	}
	switch o.Kind {
	case "BET":
		return -m.minor, ""
	case "WIN", "REFUND":
		return m.minor, ""
	case "ROLLBACK":
		if ref.Kind == "BET" {
			return m.minor, ""
		}
		return -m.minor, ""
	}
	return 0, ""
}
