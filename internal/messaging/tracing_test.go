package messaging

import (
	"context"
	"os"
	"strings"
	"testing"

	"example.com/wallet/internal/store"
	"example.com/wallet/internal/telemetry"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestOutboxTraceTransport(t *testing.T) {
	admin, client := recoveryResources(t)
	ctx := context.Background()
	schema := "tracing_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
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
	if _, err = pool.Exec(ctx, "CREATE TABLE outbox (LIKE public.outbox INCLUDING ALL)"); err != nil {
		t.Fatal(err)
	}
	q, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("trace-" + uuid.NewString() + ".fifo"), Attributes: map[string]string{"FifoQueue": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
	id := uuid.NewString()
	body := `{"eventId":"` + id + `","aggregateId":"traced-wallet"}`
	parent := "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	if _, err = pool.Exec(ctx, `INSERT INTO outbox(event_id,transaction_id,event_type,payload,traceparent) VALUES($1,$2,'test',$3,$4)`, id, uuid.NewString(), body, parent); err != nil {
		t.Fatal(err)
	}
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer provider.Shutdown(ctx)
	s := store.New(pool)
	s.Tracing = &telemetry.Tracing{Provider: provider}
	publisher := &Publisher{Consumer: &Consumer{Client: client, Store: s}, Queue: aws.ToString(q.QueueUrl)}
	if worked, err := publisher.PublishOne(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	received, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl, WaitTimeSeconds: 2, MessageAttributeNames: []string{"All"}})
	if err != nil || len(received.Messages) != 1 {
		t.Fatal("receive", err)
	}
	remote := trace.SpanContextFromContext(messageContext(ctx, received.Messages[0].MessageAttributes))
	spans := exporter.GetSpans()
	if len(spans) != 1 || remote.TraceID().String() != "0123456789abcdef0123456789abcdef" || remote.SpanID() != spans[0].SpanContext.SpanID() || spans[0].Parent.SpanID().String() != "0123456789abcdef" {
		t.Fatal("producer parent or outgoing context lost")
	}
	var unchanged bool
	if err = pool.QueryRow(ctx, `SELECT payload=$2::jsonb AND traceparent=$3 AND published_at IS NOT NULL FROM outbox WHERE event_id=$1`, id, body, parent).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal("snapshot changed", err)
	}
}
