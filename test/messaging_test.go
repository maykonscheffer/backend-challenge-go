package test

import (
	"context"
	"encoding/json"
	"example.com/wallet/internal/messaging"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"os"
	"testing"
	"time"
)

func TestSQSIntegration(t *testing.T) {
	if os.Getenv("TEST_SQS_ENDPOINT") == "" {
		t.Skip("requires MiniStack and three application consumers")
	}
	s := setup(t)
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client := sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(os.Getenv("TEST_SQS_ENDPOINT")) })
	q, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String("wager-transactions.fifo")})
	if err != nil {
		t.Fatal(err)
	}
	send := func(body, group string) {
		t.Helper()
		_, e := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: q.QueueUrl, MessageBody: &body, MessageGroupId: &group, MessageDeduplicationId: aws.String(uuid.NewString())})
		if e != nil {
			t.Fatal(e)
		}
	}
	t.Run("transient lock timeout rolls back inbox then retries", func(t *testing.T) {
		w := s.wallet(t, "30.00")
		o := op(w, "BET", "5.00")
		e := messaging.Envelope{MessageID: uuid.NewString(), Type: "WagerTransactionRequested", OccurredAt: time.Now().UTC(), Data: messaging.Data{Operation: o, Key: uuid.NewString()}}
		tx, err := s.db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, "SELECT id FROM wallets WHERE id=$1 FOR UPDATE", w.ID); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(e)
		send(string(body), w.ID)
		// App lock_timeout is 5s. Hold the lock beyond that to force a failed attempt.
		time.Sleep(7 * time.Second)
		var seen bool
		if err = s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM inbox WHERE message_id=$1)", e.MessageID).Scan(&seen); err != nil || seen {
			t.Fatalf("inbox committed during failed operation: %v %v", seen, err)
		}
		if err = tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			_ = s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM inbox WHERE message_id=$1)", e.MessageID).Scan(&seen)
			if seen {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !seen {
			t.Fatal("transient operation did not recover")
		}
		s.check(t, w, "25.00", 2)
	})
	t.Run("same envelope delivered repeatedly and shared HTTP idempotency", func(t *testing.T) {
		w := s.wallet(t, "100.00")
		o := op(w, "BET", "10.00")
		key := uuid.NewString()
		envelope := messaging.Envelope{MessageID: uuid.NewString(), Type: "WagerTransactionRequested", OccurredAt: time.Now().UTC(), Data: messaging.Data{Operation: o, Key: key}}
		b, _ := json.Marshal(envelope)
		// Deliberately vary transport deduplication IDs to exercise APPLICATION deduplication.
		send(string(b), w.ID)
		send(string(b), w.ID)
		send(string(b), w.ID)
		original := s.submit(t, o, key, 200)
		deadline := time.Now().Add(30 * time.Second)
		deliveries := 0
		for time.Now().Before(deadline) {
			_ = s.db.QueryRow(ctx, "SELECT deliveries FROM inbox WHERE consumer='wager' AND message_id=$1", envelope.MessageID).Scan(&deliveries)
			if deliveries == 3 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if deliveries != 3 {
			t.Fatalf("only %d deliveries were committed to inbox", deliveries)
		}
		replay := s.submit(t, o, key, 200)
		if !replay.Replay || original.TransactionID != replay.TransactionID {
			t.Fatal("HTTP/SQS result differs")
		}
		s.check(t, w, "90.00", 2)
		// Reuse envelope identity with different business content: must go to DLQ.
		envelope.Data.Money.Amount = "11.00"
		bad, _ := json.Marshal(envelope)
		send(string(bad), w.ID)
		dlq, e := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String("wager-transactions-dlq.fifo")})
		if e != nil {
			t.Fatal(e)
		}
		found := false
		deadline = time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			out, e := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: dlq.QueueUrl, WaitTimeSeconds: 1, MaxNumberOfMessages: 10})
			if e != nil {
				t.Fatal(e)
			}
			for _, m := range out.Messages {
				var got messaging.Envelope
				if json.Unmarshal([]byte(aws.ToString(m.Body)), &got) == nil && got.MessageID == envelope.MessageID {
					found = true
					_, e = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: dlq.QueueUrl, ReceiptHandle: m.ReceiptHandle})
					if e != nil {
						t.Fatal(e)
					}
				}
			}
			if found {
				break
			}
		}
		if !found {
			t.Fatal("conflicting envelope did not arrive at DLQ")
		}
		s.check(t, w, "90.00", 2)
	})
	t.Run("SQS first then HTTP replay", func(t *testing.T) {
		w := s.wallet(t, "50.00")
		o := op(w, "WIN", "5.00")
		key := uuid.NewString()
		e := messaging.Envelope{MessageID: uuid.NewString(), Type: "WagerTransactionRequested", OccurredAt: time.Now().UTC(), Data: messaging.Data{Operation: o, Key: key}}
		b, _ := json.Marshal(e)
		send(string(b), w.ID)
		seen := false
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			_ = s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM inbox WHERE message_id=$1)", e.MessageID).Scan(&seen)
			if seen {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !seen {
			t.Fatal("SQS operation not committed")
		}
		r := s.submit(t, o, key, 200)
		if !r.Replay || r.Balance.Amount != "55.00" {
			t.Fatal(r)
		}
		s.check(t, w, "55.00", 2)
	})
	t.Run("SQS pending is durable before reference arrives", func(t *testing.T) {
		w := s.wallet(t, "50.00")
		bet := op(w, "BET", "5.00")
		o := op(w, "REFUND", "5.00")
		o.Reference = bet.ExternalID
		key := uuid.NewString()
		e := messaging.Envelope{MessageID: uuid.NewString(), Type: "WagerTransactionRequested", OccurredAt: time.Now().UTC(), Data: messaging.Data{Operation: o, Key: key}}
		b, _ := json.Marshal(e)
		send(string(b), w.ID)
		seen := false
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM inbox WHERE message_id=$1)", e.MessageID).Scan(&seen); err != nil {
				t.Fatal(err)
			}
			if seen {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !seen {
			t.Fatal("pending message not committed")
		}
		pending := s.submit(t, o, key, 202)
		if !pending.Replay {
			t.Fatal("pending SQS result not shared with HTTP")
		}
		s.check(t, w, "50.00", 1)
		s.submit(t, bet, uuid.NewString(), 200)
		result := awaitTerminal(t, s, pending.TransactionID)
		if result.Status != "PROCESSED" {
			t.Fatal(result)
		}
		s.check(t, w, "50.00", 3)
	})
}
