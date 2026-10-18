package test

import (
	"context"
	"encoding/json"
	"example.com/wallet/internal/messaging"
	"example.com/wallet/internal/store"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOutboxPublication(t *testing.T) {
	if os.Getenv("TEST_SQS_ENDPOINT") == "" {
		t.Skip("requires real services")
	}
	s := setup(t)
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client := sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(os.Getenv("TEST_SQS_ENDPOINT")) })
	q, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String("wallet-events.fifo")})
	if err != nil {
		t.Fatal(err)
	}
	w := s.wallet(t, "100.00")
	o := op(w, "BET", "10.00")
	key := uuid.NewString()
	s.submit(t, o, key, 200)
	s.submit(t, o, key, 200)
	expected := map[string]string{}
	rows, err := s.db.Query(ctx, "SELECT event_id::text,payload::text FROM outbox WHERE payload->>'aggregateId'=$1 ORDER BY seq", w.ID)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for rows.Next() {
		var id, body string
		if err = rows.Scan(&id, &body); err != nil {
			t.Fatal(err)
		}
		expected[id] = body
		order = append(order, id)
	}
	rows.Close()
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	if len(order) != 4 {
		t.Fatalf("expected four events, got %d", len(order))
	}
	seen := []string{}
	deadline := time.Now().Add(45 * time.Second)
	for len(seen) < 4 && time.Now().Before(deadline) {
		out, e := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl, MaxNumberOfMessages: 10, WaitTimeSeconds: 1})
		if e != nil {
			t.Fatal(e)
		}
		for _, m := range out.Messages {
			var body map[string]any
			if json.Unmarshal([]byte(aws.ToString(m.Body)), &body) != nil {
				t.Fatal("bad event JSON")
			}
			if body["aggregateId"] == w.ID {
				id, _ := body["eventId"].(string)
				want, ok := expected[id]
				if !ok {
					t.Fatal("unexpected event", id)
				}
				var snapshot map[string]any
				_ = json.Unmarshal([]byte(want), &snapshot)
				gotJSON, _ := json.Marshal(body)
				wantJSON, _ := json.Marshal(snapshot)
				if string(gotJSON) != string(wantJSON) {
					t.Fatal("published payload differs from committed snapshot")
				}
				seen = append(seen, id)
			}
			_, e = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: q.QueueUrl, ReceiptHandle: m.ReceiptHandle})
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	if len(seen) != 4 {
		t.Fatalf("received %d/4 events", len(seen))
	}
	for i := range order {
		if seen[i] != order[i] {
			t.Fatal("wallet event order changed")
		}
	}
	var count int
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err = s.db.QueryRow(ctx, "SELECT count(*) FROM outbox WHERE payload->>'aggregateId'=$1 AND published_at IS NOT NULL", w.ID).Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count == 4 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if count != 4 {
		t.Fatal("publication not confirmed in database")
	}
	s.check(t, w, "90.00", 2)
}

func TestOutboxRetryAndLocks(t *testing.T) {
	if os.Getenv("TEST_SQS_ENDPOINT") == "" {
		t.Skip("requires real services")
	}
	s := setup(t)
	ctx := context.Background()
	// Isolated schema prevents running application publishers from taking test rows.
	schema := "publisher_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := s.db.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer s.db.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, `CREATE TABLE outbox (LIKE public.outbox INCLUDING ALL)`)
	if err != nil {
		t.Fatal(err)
	}
	acfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client := sqs.NewFromConfig(acfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(os.Getenv("TEST_SQS_ENDPOINT")) })
	q, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(schema + ".fifo"), Attributes: map[string]string{"FifoQueue": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
	for _, group := range []string{"a", "a", "b"} {
		_, err = pool.Exec(ctx, `INSERT INTO outbox(event_id,transaction_id,event_type,payload) VALUES($1,$2,'test',jsonb_build_object('aggregateId',$3::text))`, uuid.NewString(), uuid.NewString(), group)
		if err != nil {
			t.Fatal(err)
		}
	}
	p := &messaging.Publisher{Consumer: &messaging.Consumer{Client: client, Store: store.New(pool)}, Queue: aws.ToString(q.QueueUrl)}
	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err = lock.Exec(ctx, "SELECT seq FROM outbox WHERE seq=1 FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	if worked, e := p.PublishOne(ctx); e != nil || !worked {
		t.Fatalf("independent wallet blocked: %v %v", worked, e)
	}
	var published bool
	if err = pool.QueryRow(ctx, "SELECT published_at IS NOT NULL FROM outbox WHERE seq=3").Scan(&published); err != nil || !published {
		t.Fatal("wrong row published", err)
	}
	if worked, e := p.PublishOne(ctx); e != nil || worked {
		t.Fatalf("overtook locked predecessor: %v %v", worked, e)
	}
	if err = lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	valid := p.Queue
	p.Queue = valid + "-missing"
	if worked, e := p.PublishOne(ctx); e == nil || !worked {
		t.Fatal("expected broker failure")
	}
	var attempts int
	if err = pool.QueryRow(ctx, "SELECT attempts,published_at IS NOT NULL FROM outbox WHERE seq=1").Scan(&attempts, &published); err != nil || attempts != 1 || published {
		t.Fatalf("failed send was lost: %d %v %v", attempts, published, err)
	}
	p.Queue = valid
	if worked, e := p.PublishOne(ctx); e != nil || worked {
		t.Fatal("retry backoff ignored", e)
	}
	if _, err = pool.Exec(ctx, "UPDATE outbox SET next_attempt_at=now()"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if worked, e := p.PublishOne(ctx); e != nil || !worked {
			t.Fatal("recovery failed", e)
		}
	}
	if worked, e := p.PublishOne(ctx); e != nil || worked {
		t.Fatal("published rows selected again", e)
	}
	t.Run("two active publishers contend without duplicating or blocking other wallets", func(t *testing.T) {
		for _, group := range []string{"c", "d"} {
			if _, err := pool.Exec(ctx, `INSERT INTO outbox(event_id,transaction_id,event_type,payload) VALUES($1,$2,'test',jsonb_build_object('aggregateId',$3::text))`, uuid.NewString(), uuid.NewString(), group); err != nil {
				t.Fatal(err)
			}
		}
		locked, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		first := &messaging.Publisher{Consumer: p.Consumer, Queue: p.Queue, BeforeSend: func() { close(locked); <-release }}
		second := &messaging.Publisher{Consumer: p.Consumer, Queue: p.Queue}
		done := make(chan error, 1)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			worked, err := first.PublishOne(ctx)
			if err == nil && !worked {
				err = fmt.Errorf("first publisher found no work")
			}
			done <- err
		}()
		defer func() {
			unblock()
			select {
			case <-finished:
			case <-time.After(12 * time.Second):
				t.Error("publisher failed to terminate")
			}
		}()
		select {
		case <-locked:
		case err := <-done:
			t.Fatalf("publisher exited before locking: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("publisher never acquired row")
		}
		// First publisher is still active holding c. Second must publish d, never c.
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if worked, err := second.PublishOne(bounded); err != nil || !worked {
			t.Fatal("second publisher blocked", worked, err)
		}
		if worked, err := second.PublishOne(bounded); err != nil || worked {
			t.Fatal("locked event selected twice", worked, err)
		}
		var confirmed int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE payload->>'aggregateId'='d' AND published_at IS NOT NULL`).Scan(&confirmed); err != nil || confirmed != 1 {
			t.Fatal("independent wallet not published", err)
		}
		unblock()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("first publisher did not finish")
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE payload->>'aggregateId' IN ('c','d') AND published_at IS NOT NULL AND attempts=1`).Scan(&confirmed); err != nil || confirmed != 2 {
			t.Fatal("duplicate or missing publication", confirmed, err)
		}
		if first.Published.Load() != 1 || second.Published.Load() != 1 {
			t.Fatal("unexpected publisher ownership")
		}
	})
}
