package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalidTransition = errors.New("invalid transaction transition")

type TransactionStatus string

const (
	Pending          TransactionStatus = "PENDING"
	PendingReference TransactionStatus = "PENDING_REFERENCE"
	Processed        TransactionStatus = "PROCESSED"
	Rejected         TransactionStatus = "REJECTED"
	Failed           TransactionStatus = "FAILED"
)

// WagerTransaction owns business identity and lifecycle. Rehydration never executes
// a financial movement or emits events; persistence owns those atomic effects.
type WagerTransaction struct {
	id          string
	operation   Operation
	status      TransactionStatus
	failureCode string
	money       Money
	metadata    TransactionMetadata
}

// TransactionMetadata is a value snapshot; callers cannot mutate entity state through it.
type TransactionMetadata struct {
	IdempotencyKey, PayloadHash, ReferenceID string
	CreatedAt, UpdatedAt, CompletedAt        time.Time
	Balance                                  Money
	HasResult                                bool
}

func NewWagerTransaction(id string, operation Operation, key string, at time.Time) (WagerTransaction, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(key) == "" || len(key) > 200 || at.IsZero() {
		return WagerTransaction{}, ErrInvalid
	}
	m, err := operation.Validate()
	if err != nil {
		return WagerTransaction{}, err
	}
	return WagerTransaction{id: id, operation: operation, status: Pending, money: m, metadata: TransactionMetadata{IdempotencyKey: key, PayloadHash: operation.Fingerprint(), CreatedAt: at.UTC(), UpdatedAt: at.UTC()}}, nil
}
func RestoreWagerTransaction(id string, operation Operation, status TransactionStatus, code string, metadata TransactionMetadata) (WagerTransaction, error) {
	var t WagerTransaction
	var err error
	if operation.Kind == "OPENING" {
		m, e := ParseMoney(operation.Money)
		if e != nil {
			return t, e
		}
		t, err = NewOpeningTransaction(id, operation.WalletID, operation.PlayerID, m, metadata.CreatedAt)
		if err == nil && t.operation != operation {
			return WagerTransaction{}, ErrInvalid
		}
	} else {
		t, err = NewWagerTransaction(id, operation, metadata.IdempotencyKey, metadata.CreatedAt)
	}
	if err != nil {
		return t, err
	}
	switch status {
	case Pending, Processed:
		if code != "" {
			return WagerTransaction{}, ErrInvalid
		}
	case PendingReference:
		if operation.Reference == "" || code != "REFERENCE_PENDING" {
			return WagerTransaction{}, ErrInvalid
		}
	case Rejected, Failed:
		if strings.TrimSpace(code) == "" {
			return WagerTransaction{}, ErrInvalid
		}
	default:
		return WagerTransaction{}, ErrInvalid
	}
	terminal := status == Processed || status == Rejected || status == Failed
	if metadata.PayloadHash != t.metadata.PayloadHash || metadata.IdempotencyKey != t.metadata.IdempotencyKey || metadata.UpdatedAt.Before(metadata.CreatedAt) ||
		(terminal && (!metadata.HasResult || metadata.CompletedAt.Before(metadata.UpdatedAt))) || (!terminal && !metadata.CompletedAt.IsZero()) ||
		(metadata.HasResult && (!metadata.Balance.valid() || metadata.Balance.minor < 0 || metadata.Balance.currency != t.money.currency)) ||
		(metadata.ReferenceID != "" && operation.Reference == "") || (operation.Kind == "OPENING" && status != Pending && status != Processed) {
		return WagerTransaction{}, ErrInvalid
	}
	if operation.Kind == "OPENING" && metadata.HasResult && metadata.Balance != t.money {
		return WagerTransaction{}, ErrInvalid
	}
	t.status = status
	t.failureCode = code
	t.metadata = metadata
	return t, nil
}
func NewOpeningTransaction(id, wallet, player string, m Money, at time.Time) (WagerTransaction, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(wallet) == "" || strings.TrimSpace(player) == "" || !m.valid() || m.minor <= 0 || at.IsZero() {
		return WagerTransaction{}, ErrInvalid
	}
	dto, err := m.JSON()
	if err != nil {
		return WagerTransaction{}, err
	}
	return WagerTransaction{id: id, operation: Operation{WalletID: wallet, PlayerID: player, Kind: "OPENING", Money: dto}, money: m, status: Pending, metadata: TransactionMetadata{CreatedAt: at.UTC(), UpdatedAt: at.UTC()}}, nil
}
func (t WagerTransaction) Money() Money                  { return t.money }
func (t WagerTransaction) Metadata() TransactionMetadata { return t.metadata }
func (t *WagerTransaction) SetResult(balance Money, referenceID string) error {
	if t.id == "" || (t.status != Pending && t.status != PendingReference) {
		return ErrInvalidTransition
	}
	if !balance.valid() || balance.minor < 0 || balance.currency != t.money.currency || (referenceID != "" && t.operation.Reference == "") {
		return ErrInvalid
	}
	if t.operation.Kind == "OPENING" && balance != t.money {
		return ErrInvalid
	}
	t.metadata.Balance = balance
	t.metadata.ReferenceID = referenceID
	t.metadata.HasResult = true
	return nil
}
func (t WagerTransaction) ID() string                { return t.id }
func (t WagerTransaction) Operation() Operation      { return t.operation }
func (t WagerTransaction) Status() TransactionStatus { return t.status }
func (t WagerTransaction) FailureCode() string       { return t.failureCode }
func (t *WagerTransaction) transition(next TransactionStatus, code string) error {
	if t.id == "" || (t.status != Pending && t.status != PendingReference) {
		return fmt.Errorf("%w: %s to %s", ErrInvalidTransition, t.status, next)
	}
	metadata := t.metadata
	at := time.Now().UTC()
	if at.Before(metadata.UpdatedAt) {
		at = metadata.UpdatedAt
	}
	metadata.UpdatedAt = at
	if next == Processed || next == Rejected || next == Failed {
		metadata.CompletedAt = at
	}
	candidate, err := RestoreWagerTransaction(t.id, t.operation, next, code, metadata)
	if err != nil {
		return err
	}
	*t = candidate
	return nil
}
func (t *WagerTransaction) WaitForReference() error {
	return t.transition(PendingReference, "REFERENCE_PENDING")
}
func (t *WagerTransaction) Process() error           { return t.transition(Processed, "") }
func (t *WagerTransaction) Reject(code string) error { return t.transition(Rejected, code) }
func (t *WagerTransaction) Fail(code string) error   { return t.transition(Failed, code) }
