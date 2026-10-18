package store

import (
	"context"
	"encoding/json"
	"example.com/wallet/internal/domain"
	"example.com/wallet/internal/telemetry"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"time"
)

type traceKey struct{}
type Trace struct{ CorrelationID, CausationID string }

func WithTrace(ctx context.Context, correlation, causation string) context.Context {
	return context.WithValue(ctx, traceKey{}, Trace{correlation, causation})
}
func trace(ctx context.Context) Trace { v, _ := ctx.Value(traceKey{}).(Trace); return v }

func event(ctx context.Context, tx pgx.Tx, r Result, wallet, kind string) error {
	d := domain.TransactionEventData{TransactionID: r.TransactionID, WalletID: wallet, Status: r.Status, Balance: r.Balance, FailureCode: r.Code}
	var amount int64
	var currency, correlation, causation string
	err := tx.QueryRow(ctx, `SELECT player_id::text,COALESCE(provider_id,''),COALESCE(external_id,''),COALESCE(round_id,''),COALESCE(game_id,''),kind,amount,currency,
 COALESCE(NULLIF(correlation_id,''),id::text),causation_id FROM transactions WHERE id=$1`, r.TransactionID).Scan(&d.PlayerID, &d.ProviderID, &d.ExternalID, &d.RoundID, &d.GameID, &d.Kind, &amount, &currency, &correlation, &causation)
	if err != nil {
		return err
	}
	d.Money, err = domain.Format(amount, currency)
	if err != nil {
		return err
	}
	id := uuid.NewString()
	at := time.Now().UTC()
	var payload any
	switch kind {
	case "WagerTransactionProcessed":
		payload = domain.NewProcessedEvent(id, correlation, causation, at, d)
	case "WagerTransactionRejected":
		payload = domain.NewRejectedEvent(id, correlation, causation, at, d)
	case "WagerTransactionFailed":
		payload = domain.NewFailedEvent(id, correlation, causation, at, d)
	case "WagerTransactionPendingReference":
		payload = domain.NewPendingReferenceEvent(id, correlation, causation, at, d)
	case "WalletBalanceChanged":
		b := domain.BalanceChangedData{WalletID: wallet, TransactionID: r.TransactionID, Money: d.Money}
		var before, after int64
		err = tx.QueryRow(ctx, `SELECT l.direction,l.before_balance,l.after_balance,w.version FROM ledger l JOIN wallets w ON w.id=l.wallet_id WHERE l.transaction_id=$1`, r.TransactionID).Scan(&b.Direction, &before, &after, &b.WalletVersion)
		if err != nil {
			return err
		}
		b.BalanceBefore, err = domain.Format(before, currency)
		if err != nil {
			return err
		}
		b.BalanceAfter, err = domain.Format(after, currency)
		if err != nil {
			return err
		}
		payload = domain.NewBalanceChangedEvent(id, correlation, causation, at, b)
	default:
		return fmt.Errorf("unknown event type %s", kind)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	carrier := telemetry.Inject(ctx)
	// Pending resolution can run after the original HTTP/SQS operation ended.
	// Reuse its durable parent without modifying the event payload or fingerprint.
	if carrier["traceparent"] == "" {
		var parent, state string
		e := tx.QueryRow(ctx, "SELECT traceparent,tracestate FROM outbox WHERE transaction_id=$1 AND traceparent<>'' ORDER BY seq LIMIT 1", r.TransactionID).Scan(&parent, &state)
		if e != nil && e != pgx.ErrNoRows {
			return e
		}
		carrier["traceparent"], carrier["tracestate"] = parent, state
	}
	_, err = tx.Exec(ctx, "INSERT INTO outbox(event_id,transaction_id,event_type,payload,traceparent,tracestate) VALUES($1,$2,$3,$4,$5,$6)", id, r.TransactionID, kind, body, carrier["traceparent"], carrier["tracestate"])
	return err
}
