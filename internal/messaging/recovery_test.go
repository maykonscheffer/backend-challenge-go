package messaging

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"example.com/wallet/internal/domain"
	"example.com/wallet/internal/store"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

func recoveryResources(t *testing.T) (*pgxpool.Pool, *sqs.Client) {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" || os.Getenv("TEST_SQS_ENDPOINT") == "" {
		t.Skip("requires real PostgreSQL and SQS")
	}
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	if schema := os.Getenv("CRASH_SCHEMA"); schema != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
	}
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	acfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return p, sqs.NewFromConfig(acfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(os.Getenv("TEST_SQS_ENDPOINT")) })
}

func TestCrashWorker(t *testing.T) {
	mode := os.Getenv("CRASH_MODE")
	if mode == "" {
		t.Skip("subprocess helper")
	}
	pool, client := recoveryResources(t)
	ctx := context.Background()
	c := &Consumer{Client: client, Store: store.New(pool), Queue: os.Getenv("CRASH_QUEUE"), allowed: map[string]bool{"provider-a": true}}
	die := func() { os.Exit(91) }
	if mode == "shutdown" {
		worker, cancel := signal.NotifyContext(ctx, syscall.SIGTERM)
		defer cancel()
		c.AfterCommit = func() { fmt.Println("COMMITTED_WAITING_FOR_SIGTERM"); <-worker.Done() }
		c.Run(worker)
		if c.Completed.Load() != 1 || c.DurationMicros.Load() <= 0 {
			t.Fatal("missing processing metrics")
		}
		return
	}
	if mode == "consumer" {
		c.AfterCommit = die
		out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: &c.Queue, WaitTimeSeconds: 2, VisibilityTimeout: 1, MaxNumberOfMessages: 1})
		if err != nil || len(out.Messages) != 1 {
			t.Fatalf("receive: %v", err)
		}
		c.Handle(ctx, out.Messages[0])
	} else {
		p := &Publisher{Consumer: c, Queue: c.Queue}
		if mode == "before-send" {
			p.BeforeSend = die
		} else {
			p.AfterSend = die
		}
		if _, err := p.PublishOne(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("fault point not reached")
}

func killAt(t *testing.T, mode, queue, schema string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestCrashWorker$", "-test.v")
	cmd.Env = append(os.Environ(), "CRASH_MODE="+mode, "CRASH_QUEUE="+queue, "CRASH_SCHEMA="+schema)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 91 {
		t.Fatalf("worker did not crash at requested boundary: %v %s", err, output)
	}
}

func TestCrashRecovery(t *testing.T) {
	pool, client := recoveryResources(t)
	ctx := context.Background()
	queue := func() string {
		q, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("recovery-" + uuid.NewString() + ".fifo"), Attributes: map[string]string{"FifoQueue": "true"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl}) })
		return aws.ToString(q.QueueUrl)
	}
	t.Run("SIGTERM finishes active message and stops polling", func(t *testing.T) {
		q := queue()
		repo := store.New(pool)
		money, _ := domain.ParseMoney(domain.MoneyJSON{Amount: "100.00", Currency: "BRL"})
		w, err := repo.Open(ctx, uuid.NewString(), money)
		if err != nil {
			t.Fatal(err)
		}
		o := domain.Operation{ProviderID: "provider-a", ExternalID: uuid.NewString(), WalletID: w.ID, PlayerID: w.PlayerID, RoundID: "shutdown", GameID: "game", Kind: "BET", Money: domain.MoneyJSON{Amount: "5.00", Currency: "BRL"}}
		e := Envelope{MessageID: uuid.NewString(), Type: "WagerTransactionRequested", OccurredAt: time.Now().UTC(), Data: Data{Operation: o, Key: uuid.NewString()}}
		body, _ := json.Marshal(e)
		send := func(body []byte) {
			t.Helper()
			if _, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: &q, MessageBody: aws.String(string(body)), MessageGroupId: &w.ID, MessageDeduplicationId: aws.String(uuid.NewString())}); err != nil {
				t.Fatal(err)
			}
		}
		send(body)
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(bounded, exe, "-test.run=^TestCrashWorker$", "-test.v")
		cmd.Env = append(os.Environ(), "CRASH_MODE=shutdown", "CRASH_QUEUE="+q)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}()
		scanner := bufio.NewScanner(stdout)
		ready := false
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "COMMITTED_WAITING_FOR_SIGTERM") {
				ready = true
				break
			}
		}
		if !ready {
			t.Fatal("active commit boundary not reached")
		}
		// Queue a second operation while the first is active; shutdown must not take it.
		e.MessageID = uuid.NewString()
		e.Data.Key = uuid.NewString()
		e.Data.ExternalID = uuid.NewString()
		second, _ := json.Marshal(e)
		send(second)
		if err = cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		for scanner.Scan() {
		} // Drain output before Wait, so subprocess pipes cannot block.
		if err = cmd.Wait(); err != nil {
			t.Fatal("unclean shutdown", err)
		}
		out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: &q, WaitTimeSeconds: 2, MaxNumberOfMessages: 1})
		if err != nil || len(out.Messages) != 1 || aws.ToString(out.Messages[0].Body) != string(second) {
			t.Fatal("first was not ACKed or second was consumed", err)
		}
		if _, err = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: &q, ReceiptHandle: out.Messages[0].ReceiptHandle}); err != nil {
			t.Fatal(err)
		}
		r, err := repo.Reconcile(ctx, w.ID)
		if err != nil || !r.Consistent || r.Stored.Amount != "95.00" || r.Entries != 2 {
			t.Fatal(r, err)
		}
	})
	t.Run("consumer exits after commit before delete", func(t *testing.T) {
		q := queue()
		s := store.New(pool)
		money, _ := domain.ParseMoney(domain.MoneyJSON{Amount: "100.00", Currency: "BRL"})
		w, err := s.Open(ctx, uuid.NewString(), money)
		if err != nil {
			t.Fatal(err)
		}
		o := domain.Operation{ProviderID: "provider-a", ExternalID: uuid.NewString(), WalletID: w.ID, PlayerID: w.PlayerID, RoundID: "crash", GameID: "game", Kind: "BET", Money: domain.MoneyJSON{Amount: "5.00", Currency: "BRL"}}
		e := Envelope{MessageID: uuid.NewString(), Type: "WagerTransactionRequested", OccurredAt: time.Now().UTC(), Data: Data{Operation: o, Key: uuid.NewString()}}
		b, _ := json.Marshal(e)
		if _, err = client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: &q, MessageBody: aws.String(string(b)), MessageGroupId: &w.ID, MessageDeduplicationId: aws.String(uuid.NewString())}); err != nil {
			t.Fatal(err)
		}
		killAt(t, "consumer", q, "")
		c := &Consumer{Client: client, Store: s, Queue: q, allowed: map[string]bool{"provider-a": true}}
		deadline := time.Now().Add(8 * time.Second)
		redelivered := false
		for time.Now().Before(deadline) {
			out, e := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: &q, WaitTimeSeconds: 1, MaxNumberOfMessages: 1})
			if e != nil {
				t.Fatal(e)
			}
			if len(out.Messages) > 0 {
				c.Handle(ctx, out.Messages[0])
				redelivered = true
				break
			}
		}
		if !redelivered {
			t.Fatal("message lost after commit")
		}
		var deliveries int
		if err = pool.QueryRow(ctx, "SELECT deliveries FROM inbox WHERE message_id=$1", e.MessageID).Scan(&deliveries); err != nil || deliveries != 2 {
			t.Fatal("application did not receive duplicate", deliveries, err)
		}
		r, err := s.Reconcile(ctx, w.ID)
		if err != nil || !r.Consistent || r.Stored.Amount != "95.00" || r.Entries != 2 {
			t.Fatal(r, err)
		}
	})
	for _, mode := range []string{"before-send", "after-send"} {
		t.Run(mode, func(t *testing.T) {
			schema := "recovery_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
				t.Fatal(err)
			}
			defer pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
			cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
			if err != nil {
				t.Fatal(err)
			}
			cfg.ConnConfig.RuntimeParams["search_path"] = schema
			isolated, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer isolated.Close()
			if _, err = isolated.Exec(ctx, "CREATE TABLE outbox (LIKE public.outbox INCLUDING ALL)"); err != nil {
				t.Fatal(err)
			}
			id := uuid.NewString()
			body, _ := json.Marshal(map[string]string{"eventId": id, "aggregateId": "wallet-recovery"})
			if _, err = isolated.Exec(ctx, "INSERT INTO outbox(event_id,transaction_id,event_type,payload) VALUES($1,$2,'test',$3)", id, uuid.NewString(), body); err != nil {
				t.Fatal(err)
			}
			q := queue()
			killAt(t, mode, q, schema)
			var pending bool
			if err = isolated.QueryRow(ctx, "SELECT published_at IS NULL FROM outbox WHERE event_id=$1", id).Scan(&pending); err != nil || !pending {
				t.Fatal("event lost after worker exit", err)
			}
			p := &Publisher{Consumer: &Consumer{Client: client, Store: store.New(isolated)}, Queue: q}
			if worked, err := p.PublishOne(ctx); err != nil || !worked {
				t.Fatal("new publisher could not recover", err)
			}
			out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: &q, WaitTimeSeconds: 1, MaxNumberOfMessages: 1})
			if err != nil || len(out.Messages) != 1 {
				t.Fatalf("missing event: %v", err)
			}
			var got map[string]string
			if err = json.Unmarshal([]byte(aws.ToString(out.Messages[0].Body)), &got); err != nil || got["eventId"] != id {
				t.Fatal("event identity changed", err)
			}
			if err = isolated.QueryRow(ctx, "SELECT published_at IS NULL FROM outbox WHERE event_id=$1", id).Scan(&pending); err != nil || pending {
				t.Fatal("publication not confirmed", err)
			}
		})
	}
}
