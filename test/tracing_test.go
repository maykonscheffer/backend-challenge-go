package test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"example.com/wallet/internal/messaging"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
)

// Opt-in integration: real API, PostgreSQL, SQS and Jaeger OTLP ingestion/query.
func TestTracingEndToEnd(t *testing.T) {
	query := os.Getenv("TEST_TRACE_QUERY_URL")
	if query == "" {
		t.Skip("requires compose.tracing.yaml and TEST_TRACE_QUERY_URL")
	}
	s := setup(t)
	ctx := context.Background()
	for _, transport := range []string{"http", "sqs"} {
		t.Run(transport, func(t *testing.T) {
			w := s.wallet(t, "100.00")
			o := op(w, "BET", "5.00")
			key := uuid.NewString()
			traceID := strings.ReplaceAll(uuid.NewString(), "-", "")
			parent := "00-" + traceID + "-0123456789abcdef-01"
			rootName := "POST /wagering/transactions"
			if transport == "http" {
				body, _ := json.Marshal(o)
				req, err := http.NewRequest("POST", s.urls[0]+"/wagering/transactions", bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+s.a)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", key)
				req.Header.Set("traceparent", parent)
				res, err := s.client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
				if res.StatusCode != 200 {
					t.Fatal(res.StatusCode)
				}
			} else {
				rootName = "wager-transactions process"
				cfg, err := config.LoadDefaultConfig(ctx)
				if err != nil {
					t.Fatal(err)
				}
				client := sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(os.Getenv("TEST_SQS_ENDPOINT")) })
				q, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String("wager-transactions.fifo")})
				if err != nil {
					t.Fatal(err)
				}
				env := messaging.Envelope{MessageID: uuid.NewString(), Type: "WagerTransactionRequested", OccurredAt: time.Now().UTC(), Data: messaging.Data{Operation: o, Key: key}}
				body, _ := json.Marshal(env)
				_, err = client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: q.QueueUrl, MessageBody: aws.String(string(body)), MessageGroupId: &w.ID, MessageDeduplicationId: aws.String(uuid.NewString()), MessageAttributes: map[string]types.MessageAttributeValue{"traceparent": {DataType: aws.String("String"), StringValue: &parent}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			type span struct {
				SpanID     string `json:"spanID"`
				Operation  string `json:"operationName"`
				References []struct {
					SpanID string `json:"spanID"`
				} `json:"references"`
			}
			deadline := time.Now().Add(15 * time.Second)
			found := false
			for time.Now().Before(deadline) {
				res, err := s.client.Get(query + "/api/traces/" + traceID)
				if err != nil {
					t.Fatal(err)
				}
				var result struct {
					Data []struct {
						Spans []span `json:"spans"`
					} `json:"data"`
				}
				err = json.NewDecoder(res.Body).Decode(&result)
				res.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Data) > 0 {
					root := ""
					for _, sp := range result.Data[0].Spans {
						if sp.Operation == rootName && len(sp.References) > 0 && sp.References[0].SpanID == "0123456789abcdef" {
							root = sp.SpanID
						}
					}
					children := 0
					for _, sp := range result.Data[0].Spans {
						if root != "" && sp.Operation == "wallet-events publish" && len(sp.References) > 0 && sp.References[0].SpanID == root {
							children++
						}
					}
					if children >= 2 {
						found = true
						break
					}
				}
				time.Sleep(200 * time.Millisecond)
			}
			if !found {
				t.Fatal("missing root and two outbox child spans in collector")
			}
			var count int
			err := s.db.QueryRow(ctx, `SELECT count(*) FROM outbox o JOIN transactions t ON t.id=o.transaction_id WHERE t.external_id=$1 AND traceparent LIKE $2 AND NOT (payload ? 'traceparent')`, o.ExternalID, "00-"+traceID+"-%").Scan(&count)
			if err != nil || count != 2 {
				t.Fatalf("durable context/payload: %d %v", count, err)
			}
			replay := s.submit(t, o, key, 200)
			if !replay.Replay {
				t.Fatal("tracing changed idempotency")
			}
			s.check(t, w, "95.00", 2)

		})
	}
}
