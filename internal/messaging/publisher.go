package messaging

import (
	"context"
	"errors"
	"example.com/wallet/internal/telemetry"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5"
	"go.uber.org/fx"
)

type Publisher struct {
	// Fault-injection seams for subprocess tests; nil in the application.
	BeforeSend, AfterSend func()
	Consumer              *Consumer
	Queue                 string
	Published, Retries    atomic.Int64
}

func NewPublisher(lc fx.Lifecycle, c *Consumer) *Publisher {
	p := &Publisher{Consumer: c}
	var cancel context.CancelFunc
	var done chan struct{}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		q, err := c.Client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("wallet-events.fifo"), Attributes: map[string]string{"FifoQueue": "true"}})
		if err != nil {
			return err
		}
		p.Queue = aws.ToString(q.QueueUrl)
		var worker context.Context
		worker, cancel = context.WithCancel(context.Background())
		done = make(chan struct{})
		go func() {
			defer close(done)
			for worker.Err() == nil {
				worked, err := p.PublishOne(worker)
				if err != nil && worker.Err() == nil {
					slog.Warn("outbox publish failed", "error", err)
				}
				if (!worked || err != nil) && !wait(worker, 250*time.Millisecond) {
					return
				}
			}
		}()
		return nil
	}, OnStop: func(ctx context.Context) error {
		if cancel == nil {
			return nil
		}
		cancel()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	return p
}

func (p *Publisher) Ready(ctx context.Context) error {
	_, err := p.Consumer.Client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: &p.Queue, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	return err
}

// PublishOne holds only an outbox row lock during the bounded network request.
// Process death releases the lock. A send followed by failed commit can redeliver
// the same eventId: downstream consumers must deduplicate it durably.
func (p *Publisher) PublishOne(parent context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	tx, err := p.Consumer.Store.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.Background())
	var seq int64
	var id, group, body, traceparent, tracestate string
	var attempts int
	err = tx.QueryRow(ctx, `SELECT o.seq,o.event_id::text,o.payload->>'aggregateId',o.payload::text,o.attempts,o.traceparent,o.tracestate
 FROM outbox o WHERE o.published_at IS NULL AND o.next_attempt_at<=now()
 AND NOT EXISTS(SELECT 1 FROM outbox older WHERE older.published_at IS NULL
 AND older.payload->>'aggregateId'=o.payload->>'aggregateId' AND older.seq<o.seq)
 ORDER BY o.seq LIMIT 1 FOR UPDATE OF o SKIP LOCKED`).Scan(&seq, &id, &group, &body, &attempts, &traceparent, &tracestate)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ctx, span := p.Consumer.Store.Tracing.Start(telemetry.Extract(ctx, propagation.MapCarrier{"traceparent": traceparent, "tracestate": tracestate}), "wallet-events publish", trace.SpanKindProducer)
	defer span.End()
	sendCtx, sendCancel := context.WithTimeout(ctx, 5*time.Second)
	if p.BeforeSend != nil {
		p.BeforeSend()
	}
	_, sendErr := p.Consumer.Client.SendMessage(sendCtx, &sqs.SendMessageInput{QueueUrl: &p.Queue, MessageBody: &body, MessageGroupId: &group, MessageDeduplicationId: &id, MessageAttributes: messageAttributes(sendCtx)})
	sendCancel()
	if sendErr != nil {
		span.SetStatus(codes.Error, "publish failed")
	}
	if sendErr == nil && p.AfterSend != nil {
		p.AfterSend()
	}
	if sendErr != nil {
		_, err = tx.Exec(ctx, `UPDATE outbox SET attempts=attempts+1,next_attempt_at=now()+($2 * interval '1 second') WHERE seq=$1`, seq, 1<<min(attempts+1, 6))
	} else {
		_, err = tx.Exec(ctx, `UPDATE outbox SET attempts=attempts+1,published_at=now() WHERE seq=$1`, seq)
	}
	if err != nil {
		return true, err
	}
	if err = tx.Commit(ctx); err != nil {
		return true, err
	}
	if sendErr != nil {
		p.Retries.Add(1)
		return true, sendErr
	}
	p.Published.Add(1)
	return true, nil
}
