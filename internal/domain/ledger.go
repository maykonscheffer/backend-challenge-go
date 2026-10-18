package domain

import (
	"strings"
	"time"
)

type WalletLedgerEntry struct {
	id, transactionID, walletID, direction string
	money, before, after                   Money
	createdAt                              time.Time
}

func NewLedgerEntry(id, transactionID, walletID, direction string, money, before, after Money, at time.Time) (WalletLedgerEntry, error) {
	for _, v := range []string{id, transactionID, walletID} {
		if strings.TrimSpace(v) == "" {
			return WalletLedgerEntry{}, ErrInvalid
		}
	}
	if at.IsZero() || !money.valid() || money.minor <= 0 || before.minor < 0 || after.minor < 0 {
		return WalletLedgerEntry{}, ErrInvalid
	}
	if err := money.compatible(before); err != nil {
		return WalletLedgerEntry{}, err
	}
	if err := money.compatible(after); err != nil {
		return WalletLedgerEntry{}, err
	}
	var expected Money
	var err error
	switch direction {
	case "CREDIT":
		expected, err = before.Add(money)
	case "DEBIT":
		expected, err = before.Subtract(money)
	default:
		return WalletLedgerEntry{}, ErrInvalid
	}
	if err != nil {
		return WalletLedgerEntry{}, err
	}
	if expected != after {
		return WalletLedgerEntry{}, ErrInvalid
	}
	return WalletLedgerEntry{id, transactionID, walletID, direction, money, before, after, at.UTC()}, nil
}
func RestoreLedgerEntry(id, transactionID, walletID, direction string, money, before, after Money, at time.Time) (WalletLedgerEntry, error) {
	return NewLedgerEntry(id, transactionID, walletID, direction, money, before, after, at)
}
func (e WalletLedgerEntry) ID() string            { return e.id }
func (e WalletLedgerEntry) TransactionID() string { return e.transactionID }
func (e WalletLedgerEntry) WalletID() string      { return e.walletID }
func (e WalletLedgerEntry) Direction() string     { return e.direction }
func (e WalletLedgerEntry) Money() Money          { return e.money }
func (e WalletLedgerEntry) BalanceBefore() Money  { return e.before }
func (e WalletLedgerEntry) BalanceAfter() Money   { return e.after }
func (e WalletLedgerEntry) CreatedAt() time.Time  { return e.createdAt }
