package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/media-service/media-platform/internal/config"
	"github.com/media-service/media-platform/internal/telemetry"
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
)

// Event types
const (
	EventMediaUploaded      = "media.uploaded"
	EventTranscodeCompleted = "media.transcode.completed"
)

type Event struct {
	Type      string    `json:"type"`
	MediaID   string    `json:"media_id"`
	Key       string    `json:"key"`       // S3 object key
	Format    string    `json:"format"`    // "hls", "dash"
	Timestamp time.Time `json:"timestamp"`
}

type Producer struct {
	writer *kafka.Writer
}

func NewProducer(cfg config.KafkaConfig) *Producer {
	return &Producer{
		writer: &kafka.Writer{
			Addr:         kafka.TCP(cfg.Brokers...),
			Topic:        cfg.Topic,
			Balancer:     &kafka.LeastBytes{},
			BatchTimeout: 10 * time.Millisecond,
			RequiredAcks: kafka.RequireOne,
		},
	}
}

func (p *Producer) Publish(ctx context.Context, evt Event) error {
	ctx, span := telemetry.Tracer.Start(ctx, "kafka.publish")
	defer span.End()
	span.SetAttributes(attribute.String("messaging.event_type", evt.Type), attribute.String("messaging.media_id", evt.MediaID))

	evt.Timestamp = time.Now()
	data, err := json.Marshal(evt)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("marshal event: %w", err)
	}

	// Inject trace context into Kafka headers
	headers := make([]kafka.Header, 0, 4)
	otel.GetTextMapPropagator().Inject(ctx, &kafkaHeaderCarrier{headers: &headers})

	return p.writer.WriteMessages(ctx, kafka.Message{
		Key:     []byte(evt.MediaID),
		Value:   data,
		Headers: headers,
	})
}

func (p *Producer) Close() error { return p.writer.Close() }

type Consumer struct {
	reader *kafka.Reader
}

func NewConsumer(cfg config.KafkaConfig, groupID string) *Consumer {
	return &Consumer{
		reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers:        cfg.Brokers,
			Topic:          cfg.Topic,
			GroupID:        groupID,
			MinBytes:       1,
			MaxBytes:       10e6,
			CommitInterval: time.Second,
			StartOffset:    kafka.FirstOffset,
		}),
	}
}

func (c *Consumer) Consume(ctx context.Context, handler func(Event) error) {
	for {
		msg, err := c.reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[kafka] read error: %v", err)
			time.Sleep(time.Second)
			continue
		}

		// Extract trace context from Kafka headers
		pctx := otel.GetTextMapPropagator().Extract(ctx, &kafkaHeaderCarrier{headers: &msg.Headers})
		pctx, span := telemetry.Tracer.Start(pctx, "kafka.consume")
		span.SetAttributes(attribute.Int64("messaging.offset", msg.Offset))

		var evt Event
		if err := json.Unmarshal(msg.Value, &evt); err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.End()
			log.Printf("[kafka] unmarshal error: %v", err)
			continue
		}
		span.SetAttributes(attribute.String("messaging.event_type", evt.Type), attribute.String("messaging.media_id", evt.MediaID))

		if err := handler(evt); err != nil {
			span.SetStatus(codes.Error, err.Error())
			log.Printf("[kafka] handler error for %s: %v", evt.MediaID, err)
		}
		span.End()
	}
}

func (c *Consumer) Close() error { return c.reader.Close() }

// kafkaHeaderCarrier adapts kafka.Header slice to propagation.TextMapCarrier
type kafkaHeaderCarrier struct {
	headers *[]kafka.Header
}

func (c *kafkaHeaderCarrier) Get(key string) string {
	for _, h := range *c.headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

func (c *kafkaHeaderCarrier) Set(key, val string) {
	*c.headers = append(*c.headers, kafka.Header{Key: key, Value: []byte(val)})
}

func (c *kafkaHeaderCarrier) Keys() []string {
	keys := make([]string, len(*c.headers))
	for i, h := range *c.headers {
		keys[i] = h.Key
	}
	return keys
}

// Ensure interface compliance
var _ propagation.TextMapCarrier = (*kafkaHeaderCarrier)(nil)
