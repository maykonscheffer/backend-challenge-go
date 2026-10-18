package domain

import "time"

type Event[T any] struct {
	EventID       string    `json:"eventId"`
	EventType     string    `json:"eventType"`
	AggregateID   string    `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   string    `json:"causationId,omitempty"`
	OccurredAt    time.Time `json:"occurredAt"`
	Version       int       `json:"version"`
	Data          T         `json:"data"`
}
type TransactionEventData struct {
	TransactionID string    `json:"transactionId"`
	WalletID      string    `json:"walletId"`
	PlayerID      string    `json:"playerId"`
	ProviderID    string    `json:"providerId,omitempty"`
	ExternalID    string    `json:"externalTransactionId,omitempty"`
	RoundID       string    `json:"roundId,omitempty"`
	GameID        string    `json:"gameId,omitempty"`
	Kind          string    `json:"kind"`
	Money         MoneyJSON `json:"money"`
	Status        string    `json:"status"`
	Balance       MoneyJSON `json:"balance"`
	FailureCode   string    `json:"failureCode,omitempty"`
}
type BalanceChangedData struct {
	WalletID      string    `json:"walletId"`
	TransactionID string    `json:"transactionId"`
	Direction     string    `json:"direction"`
	Money         MoneyJSON `json:"money"`
	BalanceBefore MoneyJSON `json:"balanceBefore"`
	BalanceAfter  MoneyJSON `json:"balanceAfter"`
	WalletVersion int64     `json:"walletVersion"`
}

type WagerTransactionProcessed Event[TransactionEventData]

func NewProcessedEvent(id, correlation, causation string, at time.Time, d TransactionEventData) WagerTransactionProcessed {
	return WagerTransactionProcessed{id, "WagerTransactionProcessed", d.WalletID, correlation, causation, at.UTC(), 1, d}
}

type WagerTransactionRejected Event[TransactionEventData]

func NewRejectedEvent(id, correlation, causation string, at time.Time, d TransactionEventData) WagerTransactionRejected {
	return WagerTransactionRejected{id, "WagerTransactionRejected", d.WalletID, correlation, causation, at.UTC(), 1, d}
}

type WagerTransactionFailed Event[TransactionEventData]

func NewFailedEvent(id, correlation, causation string, at time.Time, d TransactionEventData) WagerTransactionFailed {
	return WagerTransactionFailed{id, "WagerTransactionFailed", d.WalletID, correlation, causation, at.UTC(), 1, d}
}

type WagerTransactionPendingReference Event[TransactionEventData]

func NewPendingReferenceEvent(id, correlation, causation string, at time.Time, d TransactionEventData) WagerTransactionPendingReference {
	return WagerTransactionPendingReference{id, "WagerTransactionPendingReference", d.WalletID, correlation, causation, at.UTC(), 1, d}
}

type WalletBalanceChanged Event[BalanceChangedData]

func NewBalanceChangedEvent(id, correlation, causation string, at time.Time, d BalanceChangedData) WalletBalanceChanged {
	return WalletBalanceChanged{id, "WalletBalanceChanged", d.WalletID, correlation, causation, at.UTC(), 1, d}
}
