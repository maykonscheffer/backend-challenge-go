package test

import (
	"bytes"
	"context"
	"encoding/json"
	"example.com/wallet/internal/domain"
	"example.com/wallet/internal/store"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type suite struct {
	urls           []string
	internal, a, b string
	client         *http.Client
	db             *pgxpool.Pool
}

func setup(t *testing.T) *suite {
	t.Helper()
	raw := os.Getenv("TEST_API_URLS")
	if raw == "" {
		t.Skip("integration requires TEST_API_URLS; use docker compose --profile test run --rm tests")
	}
	s := &suite{urls: strings.Split(raw, ","), client: &http.Client{Timeout: 20 * time.Second}}
	if len(s.urls) < 3 {
		t.Fatal("three independent API processes required")
	}
	endpoint := os.Getenv("TEST_TOKEN_URL")
	token := func(id string) string {
		t.Helper()
		var last string
		for i := 0; i < 60; i++ {
			r, e := s.client.PostForm(endpoint, url.Values{"grant_type": {"client_credentials"}, "client_id": {id}, "client_secret": {"local-" + id + "-secret"}})
			if e == nil {
				b, _ := io.ReadAll(r.Body)
				r.Body.Close()
				var v struct {
					Access string `json:"access_token"`
				}
				if r.StatusCode == 200 && json.Unmarshal(b, &v) == nil && v.Access != "" {
					return v.Access
				}
				last = string(b)
			} else {
				last = e.Error()
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("Keycloak not ready: %s", last)
		return ""
	}
	s.internal = token("internal-service")
	s.a = token("provider-a")
	s.b = token("provider-b")
	db, e := pgxpool.New(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	s.db = db
	t.Cleanup(db.Close)
	for _, u := range s.urls {
		code, _, e := s.call(u, "GET", "/health/ready", "", "", nil)
		if e != nil || code != 200 {
			t.Fatalf("API not ready: %s %d %v", u, code, e)
		}
	}
	return s
}
func (s *suite) call(base, method, path, token, key string, body any) (int, []byte, error) {
	var data []byte
	if body != nil {
		var e error
		data, e = json.Marshal(body)
		if e != nil {
			return 0, nil, e
		}
	}
	r, e := http.NewRequest(method, base+path, bytes.NewReader(data))
	if e != nil {
		return 0, nil, e
	}
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	resp, e := s.client.Do(r)
	if e != nil {
		return 0, nil, e
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(resp.Body)
	return resp.StatusCode, b, e
}
func (s *suite) wallet(t *testing.T, amount string) store.Wallet {
	t.Helper()
	code, b, e := s.call(s.urls[0], "POST", "/wallets", s.internal, "", map[string]any{"playerId": uuid.NewString(), "initialBalance": domain.MoneyJSON{Amount: amount, Currency: "BRL"}})
	if e != nil || code != 201 {
		t.Fatalf("open %d %s %v", code, b, e)
	}
	var w store.Wallet
	if e = json.Unmarshal(b, &w); e != nil {
		t.Fatal(e)
	}
	return w
}
func op(w store.Wallet, kind, amount string) domain.Operation {
	return domain.Operation{ProviderID: "provider-a", ExternalID: uuid.NewString(), PlayerID: w.PlayerID, WalletID: w.ID, RoundID: "round", GameID: "game", Kind: kind, Money: domain.MoneyJSON{Amount: amount, Currency: "BRL"}}
}
func (s *suite) submit(t *testing.T, o domain.Operation, key string, want int) store.Result {
	t.Helper()
	status, b, e := s.call(s.urls[0], "POST", "/wagering/transactions", s.a, key, o)
	if e != nil || status != want {
		t.Fatalf("submit %d want %d: %s %v", status, want, b, e)
	}
	var r store.Result
	if e = json.Unmarshal(b, &r); e != nil {
		t.Fatal(e)
	}
	return r
}
func (s *suite) check(t *testing.T, w store.Wallet, amount string, entries int64) {
	t.Helper()
	code, b, e := s.call(s.urls[0], "POST", "/wallets/"+w.ID+"/reconciliation", s.internal, "", nil)
	var r store.Reconciliation
	if e != nil || code != 200 || json.Unmarshal(b, &r) != nil || !r.Consistent || r.Stored.Amount != amount || r.Entries != entries {
		t.Fatalf("reconciliation %d %s %v", code, b, e)
	}
}
func TestFinancialIntegration(t *testing.T) {
	s := setup(t)
	t.Run("fifty duplicates across three processes", func(t *testing.T) {
		w := s.wallet(t, "100.00")
		o := op(w, "BET", "10.00")
		key := uuid.NewString()
		var wg sync.WaitGroup
		errs := make(chan string, 50)
		ids := make(chan string, 50)
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				code, b, e := s.call(s.urls[i%3], "POST", "/wagering/transactions", s.a, key, o)
				var r store.Result
				if e != nil || code != 200 || json.Unmarshal(b, &r) != nil {
					errs <- fmt.Sprintf("%d %s %v", code, b, e)
					return
				}
				ids <- r.TransactionID
			}(i)
		}
		wg.Wait()
		close(errs)
		close(ids)
		for e := range errs {
			t.Error(e)
		}
		unique := map[string]bool{}
		for id := range ids {
			unique[id] = true
		}
		if len(unique) != 1 {
			t.Fatalf("%d financial operations", len(unique))
		}
		s.check(t, w, "90.00", 2)
		s.submit(t, op(w, "WIN", "5.00"), uuid.NewString(), 200)
		r := s.submit(t, o, key, 200)
		if !r.Replay || r.Balance.Amount != "90.00" {
			t.Fatal("original result not preserved", r)
		}
		o.Money.Amount = "11.00"
		s.submit(t, o, key, 409)
		o.Money.Amount = "10.00"
		s.submit(t, o, uuid.NewString(), 409)
	})
	t.Run("two eighty bets on one hundred", func(t *testing.T) {
		w := s.wallet(t, "100.00")
		statuses := make(chan int, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				status, _, e := s.call(s.urls[i], "POST", "/wagering/transactions", s.a, uuid.NewString(), op(w, "BET", "80.00"))
				if e != nil {
					statuses <- 0
				} else {
					statuses <- status
				}
			}(i)
		}
		wg.Wait()
		close(statuses)
		counts := map[int]int{}
		for c := range statuses {
			counts[c]++
		}
		if counts[200] != 1 || counts[422] != 1 {
			t.Fatal(counts)
		}
		s.check(t, w, "20.00", 2)
	})
	t.Run("authorization and provider isolation", func(t *testing.T) {
		w := s.wallet(t, "10.00")
		o := op(w, "BET", "1.00")
		key := uuid.NewString()
		r := s.submit(t, o, key, 200)
		cases := []struct {
			method, path, token, key string
			body                     any
			want                     int
		}{
			{"GET", "/wallets/" + w.ID, "", "", nil, 401}, {"GET", "/wallets/" + w.ID, "invalid", "", nil, 401}, {"GET", "/wallets/" + w.ID, s.a, "", nil, 403},
			{"GET", "/wagering/transactions/" + r.TransactionID, s.b, "", nil, 404}, {"POST", "/wagering/transactions", s.b, key, o, 403},
			{"GET", "/providers/provider-a/wagering/transactions/" + o.ExternalID, s.b, "", nil, 404},
		}
		for _, c := range cases {
			code, b, e := s.call(s.urls[1], c.method, c.path, c.token, c.key, c.body)
			if e != nil || code != c.want {
				t.Fatalf("auth %s: %d %s %v", c.path, code, b, e)
			}
		}
		s.check(t, w, "9.00", 2)
	})
	t.Run("reversals loss and ledger pagination", func(t *testing.T) {
		w := s.wallet(t, "100.00")
		bet := op(w, "BET", "20.00")
		s.submit(t, bet, uuid.NewString(), 200)
		refund := op(w, "REFUND", "20.00")
		refund.Reference = bet.ExternalID
		s.submit(t, refund, uuid.NewString(), 200)
		again := refund
		again.ExternalID = uuid.NewString()
		s.submit(t, again, uuid.NewString(), 422)
		rollback := op(w, "ROLLBACK", "20.00")
		rollback.Reference = refund.ExternalID
		s.submit(t, rollback, uuid.NewString(), 200)
		s.submit(t, op(w, "LOSS", "0.00"), uuid.NewString(), 200)
		s.check(t, w, "80.00", 4)
		code, b, e := s.call(s.urls[0], "GET", "/wallets/"+w.ID+"/ledger?limit=2", s.internal, "", nil)
		var p struct {
			Items []store.Entry `json:"items"`
			Next  string        `json:"nextCursor"`
		}
		if e != nil || code != 200 || json.Unmarshal(b, &p) != nil || len(p.Items) != 2 || p.Next == "" {
			t.Fatalf("pagination %s %v", b, e)
		}
		code, b, e = s.call(s.urls[0], "GET", "/wallets/"+w.ID+"/ledger?limit=2&cursor="+p.Next, s.internal, "", nil)
		if e != nil || code != 200 || json.Unmarshal(b, &p) != nil || len(p.Items) != 2 || p.Next != "" {
			t.Fatalf("pagination second %s %v", b, e)
		}
	})
	t.Run("database guards", func(t *testing.T) {
		w := s.wallet(t, "1.00")
		ctx := context.Background()
		for _, query := range []string{"UPDATE wallets SET balance=-1 WHERE id=$1", "UPDATE wallets SET balance=999 WHERE id=$1", "UPDATE ledger SET amount=1 WHERE wallet_id=$1", "DELETE FROM ledger WHERE wallet_id=$1"} {
			if _, e := s.db.Exec(ctx, query, w.ID); e == nil {
				t.Fatalf("schema accepted %s", query)
			}
		}
		s.check(t, w, "1.00", 1)
	})
	t.Run("independent wallet is not blocked", func(t *testing.T) {
		a := s.wallet(t, "100.00")
		b := s.wallet(t, "100.00")
		ctx := context.Background()
		tx, e := s.db.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		if _, e = tx.Exec(ctx, "SELECT id FROM wallets WHERE id=$1 FOR UPDATE", a.ID); e != nil {
			t.Fatal(e)
		}
		start := time.Now()
		s.submit(t, op(b, "BET", "1.00"), uuid.NewString(), 200)
		if time.Since(start) > 4*time.Second {
			t.Fatal("independent wallet waited for unrelated lock")
		}
		s.check(t, b, "99.00", 2)
	})
}
