package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"example.com/wallet/internal/domain"
	"example.com/wallet/internal/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")
var ErrUnavailable = errors.New("temporarily unavailable")

type Store struct {
	Tracing                          *telemetry.Tracing
	Pool                             *pgxpool.Pool
	ConcurrencyConflicts, SQLRetries atomic.Int64
}

func New(p *pgxpool.Pool) *Store { return &Store{Pool: p} }

type Wallet struct {
	CreatedAt time.Time        `json:"createdAt"`
	UpdatedAt time.Time        `json:"updatedAt"`
	ID        string           `json:"id"`
	PlayerID  string           `json:"playerId"`
	Balance   domain.MoneyJSON `json:"balance"`
	Version   int64            `json:"version"`
}
type Result struct {
	TransactionID string           `json:"transactionId"`
	Status        string           `json:"status"`
	Balance       domain.MoneyJSON `json:"balance"`
	Replay        bool             `json:"idempotentReplay"`
	Code          string           `json:"code,omitempty"`
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return ErrConflict
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}
func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func (s *Store) Open(ctx context.Context, player string, m domain.Money) (Wallet, error) {
	aggregate, err := domain.NewWallet(uuid.NewString(), player, m, time.Now().UTC())
	if err != nil {
		return Wallet{}, err
	}
	balanceDTO, err := aggregate.Balance().JSON()
	if err != nil {
		return Wallet{}, err
	}
	w := Wallet{ID: aggregate.ID(), PlayerID: aggregate.PlayerID(), Balance: balanceDTO, Version: aggregate.Version(), CreatedAt: aggregate.CreatedAt(), UpdatedAt: aggregate.UpdatedAt()}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return w, classify(err)
	}
	defer rollback(tx)
	_, err = tx.Exec(ctx, "INSERT INTO wallets(id,player_id,currency,balance,version,created_at,updated_at) VALUES($1,$2,$3,$4,1,$5,$5)", w.ID, player, m.Currency(), m.Minor(), w.CreatedAt)
	if err != nil {
		return w, classify(err)
	}
	if m.Minor() > 0 {
		opening, err := domain.NewOpeningTransaction(uuid.NewString(), w.ID, player, m, w.CreatedAt)
		if err != nil {
			return w, err
		}
		if err = opening.SetResult(m, ""); err != nil {
			return w, err
		}
		if err = opening.Process(); err != nil {
			return w, err
		}
		r := Result{TransactionID: opening.ID(), Status: string(opening.Status()), Balance: balanceDTO}
		b, _ := json.Marshal(r)
		_, err = tx.Exec(ctx, "INSERT INTO transactions(id,wallet_id,player_id,currency,kind,amount,status,result,correlation_id,causation_id) VALUES($1,$2,$3,$4,'OPENING',$5,'PROCESSED',$6,$7,$8)", r.TransactionID, w.ID, player, m.Currency(), m.Minor(), b, trace(ctx).CorrelationID, trace(ctx).CausationID)
		if err != nil {
			return w, classify(err)
		}
		if err = appendMovement(ctx, tx, r, w.ID, m.Minor(), 0, m.Minor()); err != nil {
			return w, classify(err)
		}
	}
	return w, classify(tx.Commit(ctx))
}
func (s *Store) Wallet(ctx context.Context, id string) (Wallet, error) {
	var w Wallet
	var n int64
	var c string
	err := s.Pool.QueryRow(ctx, "SELECT id::text,player_id::text,currency,balance,version,created_at,updated_at FROM wallets WHERE id=$1", id).Scan(&w.ID, &w.PlayerID, &c, &n, &w.Version, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return w, classify(err)
	}
	w.Balance, err = domain.Format(n, c)
	return w, classify(err)
}

func (s *Store) Process(ctx context.Context, o domain.Operation, key string) (Result, error) {
	return s.processInput(ctx, o, key, "", "")
}

// ProcessMessage commits the inbox receipt together with the financial result.
func (s *Store) ProcessMessage(ctx context.Context, o domain.Operation, key, messageID, messageHash string) (Result, error) {
	return s.processInput(ctx, o, key, messageID, messageHash)
}
func (s *Store) processInput(ctx context.Context, o domain.Operation, key, messageID, messageHash string) (Result, error) {
	m, err := o.Validate()
	if err != nil {
		return Result{}, err
	}
	// Retry a unique-key race across different wallets. No global in-memory locks.
	for attempt := 0; attempt < 3; attempt++ {
		r, err := s.process(ctx, o, key, m, messageID, messageHash, false)
		if permanentDataError(err) {
			r, err = s.processWithFailure(ctx, o, key, m, messageID, messageHash, false, true)
		}
		var pg *pgconn.PgError
		if errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "40P01" || pg.Code == "40001") {
			if attempt < 2 {
				s.SQLRetries.Add(1)
			}
			continue
		}
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			return r, err
		}
		return r, classify(err)
	}
	return Result{}, ErrUnavailable
}
func (s *Store) process(ctx context.Context, o domain.Operation, key string, m domain.Money, messageID, messageHash string, resume bool) (Result, error) {
	return s.processWithFailure(ctx, o, key, m, messageID, messageHash, resume, false)
}
func (s *Store) processWithFailure(ctx context.Context, o domain.Operation, key string, m domain.Money, messageID, messageHash string, resume, permanentFailure bool) (result Result, err error) {
	defer func() {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "40P01" || pg.Code == "40001" || pg.Code == "55P03") {
			s.ConcurrencyConflicts.Add(1)
		}
	}()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer rollback(tx)
	var player, currency string
	var balance, version int64
	var createdAt, updatedAt time.Time
	err = tx.QueryRow(ctx, "SELECT player_id::text,currency,balance,version,created_at,updated_at FROM wallets WHERE id=$1 FOR UPDATE", o.WalletID).Scan(&player, &currency, &balance, &version, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, ErrNotFound
	}
	if err != nil {
		return Result{}, err
	}
	hash := o.Fingerprint()
	if messageID != "" {
		tag, e := tx.Exec(ctx, `INSERT INTO inbox(consumer,message_id,fingerprint) VALUES('wager', $1,$2)
 ON CONFLICT(consumer,message_id) DO UPDATE SET deliveries=inbox.deliveries+1 WHERE inbox.fingerprint=EXCLUDED.fingerprint`, messageID, messageHash)
		if e != nil {
			return Result{}, e
		}
		if tag.RowsAffected() != 1 {
			return Result{}, ErrConflict
		}
	}
	rows, err := tx.Query(ctx, "SELECT idempotency_key,fingerprint,result FROM transactions WHERE provider_id=$1 AND (idempotency_key=$2 OR external_id=$3)", o.ProviderID, key, o.ExternalID)
	if err != nil {
		return Result{}, err
	}
	var existing *Result
	conflict := false
	for rows.Next() {
		var k, h string
		var b []byte
		if err = rows.Scan(&k, &h, &b); err != nil {
			rows.Close()
			return Result{}, err
		}
		if k != key || h != hash {
			conflict = true
		}
		var r Result
		if err = json.Unmarshal(b, &r); err != nil {
			rows.Close()
			return Result{}, err
		}
		existing = &r
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Result{}, err
	}
	if conflict {
		return Result{}, ErrConflict
	}
	if existing != nil && (!resume || existing.Status != "PENDING_REFERENCE") {
		existing.Replay = true
		return *existing, tx.Commit(ctx)
	}
	var ref *domain.Reference
	var refID *string
	if o.Reference != "" {
		ref = &domain.Reference{}
		var id string
		err = tx.QueryRow(ctx, `SELECT id::text,kind,status,player_id::text,wallet_id::text,currency,round_id,amount,
   EXISTS(SELECT 1 FROM transactions r WHERE r.reference_id=t.id AND r.kind IN ('REFUND','ROLLBACK') AND r.status='PROCESSED')
   FROM transactions t WHERE provider_id=$1 AND external_id=$2`, o.ProviderID, o.Reference).Scan(&id, &ref.Kind, &ref.Status, &ref.PlayerID, &ref.WalletID, &ref.Currency, &ref.RoundID, &ref.Amount, &ref.Reversed)
		if errors.Is(err, pgx.ErrNoRows) {
			ref = nil
		} else if err != nil {
			return Result{}, err
		} else {
			refID = &id
		}
	}
	delta, decisionErr := domain.Decide(o, m, player, currency, ref)
	code := domain.FailureCode(decisionErr)
	pending := code == "REFERENCE_NOT_FOUND" || (code == "REFERENCE_NOT_PROCESSED" && ref != nil && ref.Status == "PENDING_REFERENCE")
	if permanentFailure {
		pending = false
		code = "PERMANENT_DATA_ERROR"
	}
	attempts := 0
	if existing != nil {
		var expired, due bool
		if err = tx.QueryRow(ctx, "SELECT reference_expires_at<=now(),reference_next_attempt_at<=now(),reference_attempts FROM transactions WHERE id=$1", existing.TransactionID).Scan(&expired, &due, &attempts); err != nil {
			return Result{}, err
		}
		if !due {
			return *existing, tx.Commit(ctx)
		}
		if expired {
			pending = false
			code = "REFERENCE_NOT_FOUND"
		}
	}
	restored, err := domain.FromMinor(balance, currency)
	if err != nil {
		return Result{}, err
	}
	wallet, err := domain.RestoreWallet(o.WalletID, player, restored, version, createdAt, updatedAt)
	if err != nil {
		return Result{}, err
	}
	if code == "" {
		if delta < 0 {
			code = domain.FailureCode(wallet.Debit(m))
		} else {
			code = domain.FailureCode(wallet.Credit(m))
		}
		if code == "INSUFFICIENT_FUNDS" && o.Kind == "ROLLBACK" {
			code = "REVERSAL_INSUFFICIENT_FUNDS"
		}
	}
	transaction, err := domain.NewWagerTransaction(uuid.NewString(), o, key, time.Now().UTC())
	if existing != nil {
		transaction, err = restoreTransaction(ctx, tx, o, key, *existing)
	}
	if err != nil {
		return Result{}, err
	}
	resolved := ""
	if refID != nil {
		resolved = *refID
	}
	if err = transaction.SetResult(wallet.Balance(), resolved); err != nil {
		return Result{}, err
	}
	switch {
	case permanentFailure:
		err = transaction.Fail(code)
	case pending:
		err = transaction.WaitForReference()
	case code != "":
		err = transaction.Reject(code)
	default:
		err = transaction.Process()
	}
	if err != nil {
		return Result{}, err
	}
	balanceDTO, err := wallet.Balance().JSON()
	if err != nil {
		return Result{}, err
	}
	r := Result{TransactionID: transaction.ID(), Status: string(transaction.Status()), Balance: balanceDTO, Code: transaction.FailureCode()}
	b, _ := json.Marshal(r)
	if existing != nil {
		_, err = tx.Exec(ctx, `UPDATE transactions SET status=$2,result=$3,reference_id=$4,reference_attempts=reference_attempts+1,
   reference_next_attempt_at=now()+($5 * interval '1 second') WHERE id=$1`, r.TransactionID, r.Status, b, refID, min(2<<min(attempts, 5), 60))
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO transactions(id,provider_id,external_id,idempotency_key,fingerprint,wallet_id,player_id,currency,kind,amount,round_id,game_id,reference_id,status,result,input,reference_expires_at,reference_next_attempt_at,correlation_id,causation_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,now()+interval '24 hours',now()+interval '2 seconds',$17,$18)`, r.TransactionID, o.ProviderID, o.ExternalID, key, hash, o.WalletID, o.PlayerID, o.Money.Currency, o.Kind, m.Minor(), o.RoundID, o.GameID, refID, r.Status, b, operationJSON(o), trace(ctx).CorrelationID, trace(ctx).CausationID)
	}
	if err != nil {
		return Result{}, err
	}
	if pending {
		if existing == nil {
			if err = event(ctx, tx, r, o.WalletID, "WagerTransactionPendingReference"); err != nil {
				return Result{}, err
			}
		}
		return r, tx.Commit(ctx)
	}
	if code == "" && delta != 0 {
		tag, e := tx.Exec(ctx, "UPDATE wallets SET balance=$1,version=$2 WHERE id=$3 AND version=$4", wallet.Balance().Minor(), wallet.Version(), o.WalletID, version)
		if e != nil {
			return Result{}, e
		}
		if tag.RowsAffected() != 1 {
			return Result{}, ErrUnavailable
		}
		err = appendMovement(ctx, tx, r, o.WalletID, delta, balance, wallet.Balance().Minor())
	} else {
		kind := "WagerTransactionRejected"
		if r.Status == "PROCESSED" {
			kind = "WagerTransactionProcessed"
		}
		if r.Status == "FAILED" {
			kind = "WagerTransactionFailed"
		}
		err = event(ctx, tx, r, o.WalletID, kind)
	}
	if err != nil {
		return Result{}, err
	}
	return r, tx.Commit(ctx)
}
func appendMovement(ctx context.Context, tx pgx.Tx, r Result, wallet string, delta, before, after int64) error {
	direction := "CREDIT"
	amount := delta
	if delta < 0 {
		direction = "DEBIT"
		amount = -delta
	}
	money, err := domain.FromMinor(amount, r.Balance.Currency)
	if err != nil {
		return err
	}
	beforeMoney, err := domain.FromMinor(before, r.Balance.Currency)
	if err != nil {
		return err
	}
	afterMoney, err := domain.FromMinor(after, r.Balance.Currency)
	if err != nil {
		return err
	}
	entry, err := domain.NewLedgerEntry(uuid.NewString(), r.TransactionID, wallet, direction, money, beforeMoney, afterMoney, time.Now().UTC())
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO ledger(transaction_id,wallet_id,currency,amount,direction,before_balance,after_balance,id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)", entry.TransactionID(), entry.WalletID(), entry.Money().Currency(), entry.Money().Minor(), entry.Direction(), entry.BalanceBefore().Minor(), entry.BalanceAfter().Minor(), entry.ID(), entry.CreatedAt())
	if err != nil {
		return err
	}
	if err = event(ctx, tx, r, wallet, "WagerTransactionProcessed"); err != nil {
		return err
	}
	return event(ctx, tx, r, wallet, "WalletBalanceChanged")
}
func (s *Store) Transaction(ctx context.Context, provider, id, external string) (Result, error) {
	var b []byte
	var err error
	if external != "" {
		err = s.Pool.QueryRow(ctx, "SELECT result FROM transactions WHERE provider_id=$1 AND external_id=$2", provider, external).Scan(&b)
	} else {
		err = s.Pool.QueryRow(ctx, "SELECT result FROM transactions WHERE provider_id=$1 AND id=$2", provider, id).Scan(&b)
	}
	if err != nil {
		return Result{}, classify(err)
	}
	var r Result
	err = json.Unmarshal(b, &r)
	return r, classify(err)
}

type Entry struct {
	ID            string           `json:"id"`
	BalanceBefore domain.MoneyJSON `json:"balanceBefore"`
	Sequence      int64            `json:"sequence"`
	TransactionID string           `json:"transactionId"`
	Direction     string           `json:"direction"`
	Money         domain.MoneyJSON `json:"money"`
	BalanceAfter  domain.MoneyJSON `json:"balanceAfter"`
	CreatedAt     time.Time        `json:"createdAt"`
}

func (s *Store) Ledger(ctx context.Context, wallet string, after int64, limit int) ([]Entry, error) {
	if _, err := s.Wallet(ctx, wallet); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, "SELECT seq,transaction_id::text,direction,amount,currency,after_balance,created_at,id::text,before_balance FROM ledger WHERE wallet_id=$1 AND seq>$2 ORDER BY seq LIMIT $3", wallet, after, limit)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	entries := []Entry{}
	for rows.Next() {
		var e Entry
		var amount, balance, before int64
		var currency string
		if err = rows.Scan(&e.Sequence, &e.TransactionID, &e.Direction, &amount, &currency, &balance, &e.CreatedAt, &e.ID, &before); err != nil {
			return nil, classify(err)
		}
		e.Money, err = domain.Format(amount, currency)
		if err != nil {
			return nil, err
		}
		e.BalanceAfter, err = domain.Format(balance, currency)
		if err != nil {
			return nil, err
		}
		e.BalanceBefore, err = domain.Format(before, currency)
		if err != nil {
			return nil, err
		}
		money, err := domain.ParseMoney(e.Money)
		if err != nil {
			return nil, err
		}
		beforeMoney, err := domain.ParseMoney(e.BalanceBefore)
		if err != nil {
			return nil, err
		}
		afterMoney, err := domain.ParseMoney(e.BalanceAfter)
		if err != nil {
			return nil, err
		}
		if _, err = domain.RestoreLedgerEntry(e.ID, e.TransactionID, wallet, e.Direction, money, beforeMoney, afterMoney, e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, classify(rows.Err())
}

type Reconciliation struct {
	WalletID   string           `json:"walletId"`
	Stored     domain.MoneyJSON `json:"storedBalance"`
	Calculated domain.MoneyJSON `json:"calculatedBalance"`
	Difference domain.MoneyJSON `json:"difference"`
	Consistent bool             `json:"consistent"`
	Entries    int64            `json:"checkedEntries"`
}

func (s *Store) Reconcile(ctx context.Context, wallet string) (Reconciliation, error) {
	var c string
	var stored, calculated, count int64
	// A single PostgreSQL statement uses one MVCC snapshot for wallet and ledger.
	err := s.Pool.QueryRow(ctx, `SELECT w.currency,w.balance,COALESCE(SUM(CASE WHEN l.direction='CREDIT' THEN l.amount::numeric ELSE -l.amount::numeric END),0)::bigint,COUNT(l.seq)
 FROM wallets w LEFT JOIN ledger l ON l.wallet_id=w.id WHERE w.id=$1 GROUP BY w.id`, wallet).Scan(&c, &stored, &calculated, &count)
	if err != nil {
		return Reconciliation{}, classify(err)
	}
	a, err := domain.FromMinor(stored, c)
	if err != nil {
		return Reconciliation{}, err
	}
	b, err := domain.FromMinor(calculated, c)
	if err != nil {
		return Reconciliation{}, err
	}
	difference, err := a.Subtract(b)
	if err != nil {
		return Reconciliation{}, err
	}
	r := Reconciliation{WalletID: wallet, Consistent: stored == calculated, Entries: count}
	r.Stored, err = a.JSON()
	if err != nil {
		return Reconciliation{}, err
	}
	r.Calculated, err = b.JSON()
	if err != nil {
		return Reconciliation{}, err
	}
	r.Difference, err = difference.JSON()
	return r, err
}
