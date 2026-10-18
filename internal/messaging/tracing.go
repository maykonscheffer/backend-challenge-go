package messaging

import (
	"context"
	"example.com/wallet/internal/telemetry"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.opentelemetry.io/otel/propagation"
)

func messageContext(ctx context.Context, attrs map[string]types.MessageAttributeValue) context.Context {
	carrier := propagation.MapCarrier{}
	for _, key := range []string{"traceparent", "tracestate"} {
		carrier[key] = aws.ToString(attrs[key].StringValue)
	}
	return telemetry.Extract(ctx, carrier)
}

func messageAttributes(ctx context.Context) map[string]types.MessageAttributeValue {
	attrs := map[string]types.MessageAttributeValue{}
	for key, value := range telemetry.Inject(ctx) {
		attrs[key] = types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(value)}
	}
	return attrs
}
