package test

import (
	"context"
	"encoding/json"
	"example.com/wallet/internal/messaging"
	"example.com/wallet/internal/store"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPermanentFailureOfDurableWork(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	w := s.wallet(t, "100.00")
	bet := op(w, "BET", "10.00")
	refund := op(w, "REFUND", "10.00")
	refund.Reference = bet.ExternalID
	key := uuid.NewString()
	pending := s.submit(t, refund, key, 202)
	// A real PostgreSQL data exception, restricted to this test transaction. It is
	// injected after UPDATE wallets but before the ledger insert can commit.
	name := "fault_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	id, err := uuid.Parse(pending.TransactionID)
	if err != nil {
		t.Fatal(err)
	}
	sql := `CREATE FUNCTION ` + name + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.transaction_id='` + id.String() + `'::uuid OR (NEW.wallet_id='` + w.ID + `'::uuid AND NEW.amount=500) THEN RAISE EXCEPTION USING ERRCODE='22000',MESSAGE='test data exception'; END IF;
 RETURN NEW; END $$`
	if _, err = s.db.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	defer s.db.Exec(ctx, "DROP FUNCTION "+name+"()")
	if _, err = s.db.Exec(ctx, "CREATE TRIGGER "+name+" BEFORE INSERT ON ledger FOR EACH ROW EXECUTE FUNCTION "+name+"()"); err != nil {
		t.Fatal(err)
	}
	defer s.db.Exec(ctx, "DROP TRIGGER "+name+" ON ledger")
	s.submit(t, bet, uuid.NewString(), 200)
	failed := awaitTerminal(t, s, pending.TransactionID)
	if failed.Status != "FAILED" || failed.Code != "PERMANENT_DATA_ERROR" || failed.Balance.Amount != "90.00" {
		t.Fatal(failed)
	}
	replay := s.submit(t, refund, key, 500)
	if !replay.Replay || replay.TransactionID != pending.TransactionID || replay.Code != failed.Code {
		t.Fatal(replay)
	}
	var complete bool
	var count int
	if err = s.db.QueryRow(ctx, "SELECT completed_at IS NOT NULL FROM transactions WHERE id=$1", pending.TransactionID).Scan(&complete); err != nil || !complete {
		t.Fatal("failure missing completion timestamp", err)
	}
	if err = s.db.QueryRow(ctx, "SELECT count(*) FROM outbox WHERE transaction_id=$1 AND event_type='WagerTransactionFailed'", pending.TransactionID).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed event missing or duplicated", err)
	}
	if _, err = s.db.Exec(ctx, "UPDATE transactions SET status='PENDING_REFERENCE' WHERE id=$1", pending.TransactionID); err == nil {
		t.Fatal("FAILED could be reopened")
	}
	s.check(t, w, "90.00", 2)
	t.Run("permanent failure reaches DLQ through real SQS and replays safely", func(t *testing.T) {
		if os.Getenv("TEST_SQS_ENDPOINT") == "" {
			t.Skip("requires real SQS")
		}
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		client := sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(os.Getenv("TEST_SQS_ENDPOINT")) })
		input, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String("wager-transactions.fifo")})
		if err != nil {
			t.Fatal(err)
		}
		dlq, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String("wager-transactions-dlq.fifo")})
		if err != nil {
			t.Fatal(err)
		}
		o := op(w, "BET", "5.00")
		key := uuid.NewString()
		e := messaging.Envelope{MessageID: uuid.NewString(), Type: "WagerTransactionRequested", OccurredAt: time.Now().UTC(), Data: messaging.Data{Operation: o, Key: key}}
		body, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		var original string
		for attempt := 1; attempt <= 2; attempt++ {
			_, err = client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: input.QueueUrl, MessageBody: aws.String(string(body)), MessageGroupId: aws.String(w.ID), MessageDeduplicationId: aws.String(uuid.NewString())})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			deadline := time.Now().Add(30 * time.Second)
			for !found && time.Now().Before(deadline) {
				out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: dlq.QueueUrl, WaitTimeSeconds: 1, MaxNumberOfMessages: 10, MessageAttributeNames: []string{"All"}})
				if err != nil {
					t.Fatal(err)
				}
				for _, msg := range out.Messages {
					if aws.ToString(msg.Body) != string(body) {
						continue
					}
					if aws.ToString(msg.MessageAttributes["failureReason"].StringValue) != "PERMANENT_DATA_ERROR" {
						t.Fatal("missing permanent failure reason")
					}
					found = true
					if _, err = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: dlq.QueueUrl, ReceiptHandle: msg.ReceiptHandle}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if !found {
				t.Fatal("FAILED did not reach DLQ")
			}
			r := s.submit(t, o, key, 500)
			if !r.Replay || r.Status != "FAILED" || r.Code != "PERMANENT_DATA_ERROR" {
				t.Fatal(r)
			}
			if attempt == 1 {
				original = r.TransactionID
			} else if r.TransactionID != original {
				t.Fatal("replay changed identity")
			}
			var deliveries int
			if err = s.db.QueryRow(ctx, "SELECT deliveries FROM inbox WHERE consumer='wager' AND message_id=$1", e.MessageID).Scan(&deliveries); err != nil || deliveries != attempt {
				t.Fatal("inbox delivery count", deliveries, err)
			}
			s.check(t, w, "90.00", 2)
		}
		var events int
		if err = s.db.QueryRow(ctx, "SELECT count(*) FROM outbox WHERE transaction_id=$1 AND event_type='WagerTransactionFailed'", original).Scan(&events); err != nil || events != 1 {
			t.Fatal("duplicate failure event", events, err)
		}
	})
	synchronous := op(w, "BET", "5.00")
	syncKey := uuid.NewString()
	syncResult := s.submit(t, synchronous, syncKey, 500)
	if syncResult.Status != "FAILED" || syncResult.Code != "PERMANENT_DATA_ERROR" {
		t.Fatal(syncResult)
	}
	syncReplay := s.submit(t, synchronous, syncKey, 500)
	if !syncReplay.Replay || syncReplay.TransactionID != syncResult.TransactionID {
		t.Fatal(syncReplay)
	}
	s.check(t, w, "90.00", 2)
	messageOperation := op(w, "BET", "5.00")
	messageKey := uuid.NewString()
	messageID := uuid.NewString()
	messageResult, err := store.New(s.db).ProcessMessage(ctx, messageOperation, messageKey, messageID, "test-envelope-hash")
	if err != nil || messageResult.Status != "FAILED" {
		t.Fatal(messageResult, err)
	}
	if err = s.db.QueryRow(ctx, "SELECT count(*) FROM inbox WHERE message_id=$1", messageID).Scan(&count); err != nil || count != 1 {
		t.Fatal("failure and inbox were not committed", err)
	}
	messageReplay := s.submit(t, messageOperation, messageKey, 500)
	if !messageReplay.Replay || messageReplay.TransactionID != messageResult.TransactionID {
		t.Fatal(messageReplay)
	}
	s.check(t, w, "90.00", 2)
}
