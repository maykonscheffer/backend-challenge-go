package messaging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"example.com/wallet/internal/domain"
	"example.com/wallet/internal/store"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Data struct {
	domain.Operation
	Key string `json:"idempotencyKey"`
}
type Envelope struct {
	MessageID  string    `json:"messageId"`
	Type       string    `json:"type"`
	OccurredAt time.Time `json:"occurredAt"`
	Data       Data      `json:"data"`
}

func Decode(body string) (Envelope, string, error) {
	var e Envelope
	d := json.NewDecoder(strings.NewReader(body))
	d.DisallowUnknownFields()
	if len(body) > 32<<10 || d.Decode(&e) != nil || d.Decode(&struct{}{}) != io.EOF {
		return e, "", domain.ErrInvalid
	}
	if strings.TrimSpace(e.MessageID) == "" || len(e.MessageID) > 200 || e.Type != "WagerTransactionRequested" || e.OccurredAt.IsZero() || strings.TrimSpace(e.Data.Key) == "" || len(e.Data.Key) > 200 {
		return e, "", domain.ErrInvalid
	}
	w, err := uuid.Parse(e.Data.WalletID)
	if err != nil || w == uuid.Nil {
		return e, "", domain.ErrInvalid
	}
	p, err := uuid.Parse(e.Data.PlayerID)
	if err != nil || p == uuid.Nil {
		return e, "", domain.ErrInvalid
	}
	e.Data.WalletID = w.String()
	e.Data.PlayerID = p.String()
	if _, err = e.Data.Validate(); err != nil {
		return e, "", err
	}
	// Canonical envelope identity includes key, type and time, unlike financial hash.
	b, _ := json.Marshal(map[string]any{"businessHash": e.Data.Fingerprint(), "key": e.Data.Key, "type": e.Type, "occurredAt": e.OccurredAt.UTC().Format(time.RFC3339Nano)})
	h := sha256.Sum256(bytes.TrimSpace(b))
	return e, hex.EncodeToString(h[:]), nil
}

type Consumer struct {
	// Fault-injection seam for subprocess tests; nil in the application.
	AfterCommit                        func()
	Client                             *sqs.Client
	Store                              *store.Store
	Queue, DLQ                         string
	allowed                            map[string]bool
	cancel                             context.CancelFunc
	done                               chan struct{}
	Received, Retries, DeadLetters     atomic.Int64
	Completed, DurationMicros, Replays atomic.Int64
}

func New(lc fx.Lifecycle, s *store.Store) (*Consumer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := os.Getenv("SQS_ENDPOINT")
	c := &Consumer{Client: sqs.NewFromConfig(cfg, func(o *sqs.Options) {
		if endpoint != "" {
			o.BaseEndpoint = &endpoint
		}
	}), Store: s, allowed: map[string]bool{}}
	for _, p := range strings.Split(os.Getenv("SQS_ALLOWED_PROVIDERS"), ",") {
		if p != "" {
			c.allowed[p] = true
		}
	}
	if len(c.allowed) == 0 {
		return nil, fmt.Errorf("SQS_ALLOWED_PROVIDERS required")
	}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		if err := c.Provision(ctx); err != nil {
			return err
		}
		worker, cancel := context.WithCancel(context.Background())
		c.cancel = cancel
		c.done = make(chan struct{})
		go func() { defer close(c.done); c.Run(worker) }()
		return nil
	}, OnStop: func(ctx context.Context) error {
		if c.cancel != nil {
			c.cancel()
		}
		if c.done == nil {
			return nil
		}
		select {
		case <-c.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	return c, nil
}
func (c *Consumer) Provision(ctx context.Context) error {
	dlq, err := c.Client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("wager-transactions-dlq.fifo"), Attributes: map[string]string{"FifoQueue": "true"}})
	if err != nil {
		return err
	}
	c.DLQ = aws.ToString(dlq.QueueUrl)
	attrs, err := c.Client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: &c.DLQ, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	if err != nil {
		return err
	}
	policy, _ := json.Marshal(map[string]string{"deadLetterTargetArn": attrs.Attributes["QueueArn"], "maxReceiveCount": "5"})
	q, err := c.Client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("wager-transactions.fifo"), Attributes: map[string]string{"FifoQueue": "true", "VisibilityTimeout": "30", "ReceiveMessageWaitTimeSeconds": "5", "RedrivePolicy": string(policy)}})
	if err != nil {
		return err
	}
	c.Queue = aws.ToString(q.QueueUrl)
	return nil
}
func (c *Consumer) Ready(ctx context.Context) error {
	_, err := c.Client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: &c.Queue, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	return err
}
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
func (c *Consumer) Run(ctx context.Context) {
	for ctx.Err() == nil {
		out, err := c.Client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: &c.Queue, MaxNumberOfMessages: 1, MessageAttributeNames: []string{"traceparent", "tracestate"}, WaitTimeSeconds: 5, VisibilityTimeout: 30, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount}})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("sqs receive failed", "error", err)
			if !wait(ctx, time.Second) {
				return
			}
			continue
		}
		for _, msg := range out.Messages {
			c.Handle(ctx, msg)
		}
	}
}
func (c *Consumer) Handle(parent context.Context, msg types.Message) {
	started := time.Now()
	defer func() { c.DurationMicros.Add(time.Since(started).Microseconds()); c.Completed.Add(1) }()
	c.Received.Add(1)
	// Finish the current operation on graceful shutdown; DB timeout < visibility timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	ctx, span := c.Store.Tracing.Start(messageContext(ctx, msg.MessageAttributes), "wager-transactions process", trace.SpanKindConsumer)
	defer span.End()
	e, hash, err := Decode(aws.ToString(msg.Body))
	if err == nil && !c.allowed[e.Data.ProviderID] {
		err = domain.ErrInvalid
	}
	var result store.Result
	if err == nil {
		result, err = c.Store.ProcessMessage(store.WithTrace(ctx, e.MessageID, e.MessageID), e.Data.Operation, e.Data.Key, e.MessageID, hash)
	}
	if err == nil {
		if result.Replay {
			c.Replays.Add(1)
		}
		if c.AfterCommit != nil {
			c.AfterCommit()
		}
	}
	failed := err == nil && result.Status == "FAILED"
	if err != nil || failed {
		span.SetStatus(codes.Error, "processing failed")
	}
	if err == nil && !failed {
		_, err = c.Client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: &c.Queue, ReceiptHandle: msg.ReceiptHandle})
		if err != nil {
			slog.Warn("sqs delete failed after commit", "messageId", e.MessageID, "correlationId", e.MessageID, "error", err)
		} else {
			slog.Info("sqs committed", "messageId", e.MessageID, "correlationId", e.MessageID, "transactionId", result.TransactionID, "walletId", e.Data.WalletID, "providerId", e.Data.ProviderID, "replay", result.Replay)
		}
		return
	}
	permanent := failed || errors.Is(err, domain.ErrInvalid) || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound)
	count, _ := strconv.Atoi(msg.Attributes["ApproximateReceiveCount"])
	if permanent || count >= 5 {
		reason := "RETRY_EXHAUSTED"
		if permanent {
			reason = "PERMANENT_INPUT"
		}
		if failed {
			reason = result.Code
		}
		// Send before deleting: a crash may duplicate DLQ delivery, never lose the original.
		dlqAttrs := messageAttributes(ctx)
		dlqAttrs["failureReason"] = types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(reason)}
		_, sendErr := c.Client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: &c.DLQ, MessageBody: msg.Body, MessageGroupId: aws.String("invalid"), MessageDeduplicationId: msg.MessageId, MessageAttributes: dlqAttrs})
		if sendErr == nil {
			_, sendErr = c.Client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: &c.Queue, ReceiptHandle: msg.ReceiptHandle})
			if sendErr == nil {
				c.DeadLetters.Add(1)
				return
			}
		}
		slog.Warn("sqs dlq transfer failed", "messageId", e.MessageID, "correlationId", e.MessageID, "error", sendErr)
	}
	c.Retries.Add(1)
	delay := int32(1 << min(max(count, 1), 5))
	if parent.Err() != nil {
		delay = 0
	}
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer releaseCancel()
	_, releaseErr := c.Client.ChangeMessageVisibility(releaseCtx, &sqs.ChangeMessageVisibilityInput{QueueUrl: &c.Queue, ReceiptHandle: msg.ReceiptHandle, VisibilityTimeout: delay})
	slog.Warn("sqs processing retry", "messageId", e.MessageID, "correlationId", e.MessageID, "attempt", count, "error", err, "visibilityError", releaseErr)
}
