package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/spf13/viper"

	"github.com/star-inc/armi/pkgs/contract"
	"github.com/star-inc/armi/pkgs/file"
)

var errDiscardEvent = errors.New("discard event")

// EventConsumer persists embedding statuses from a durable direct-exchange
// queue. Fanout delivery remains separate and is used only for SSE.
type EventConsumer struct {
	mu          sync.Mutex
	rabbitmqURL string
	queueName   string
	conn        *amqp.Connection
	channel     *amqp.Channel
	fileRepo    file.FileRepository
}

func NewEventConsumer(fileRepo file.FileRepository) (*EventConsumer, error) {
	if !viper.GetBool("rabbitmq.enabled") {
		slog.Info("RabbitMQ is disabled, event consumer will not start")
		return nil, nil
	}

	topology := configuredEventTopology()
	if topology.exchange == "" || topology.embeddingStatusKey == "" || topology.embeddingStatusQueue == "" {
		slog.Warn("RabbitMQ status topology is incomplete, event consumer cannot subscribe")
		return nil, nil
	}

	rabbitmqURL := viper.GetString("rabbitmq.url")
	return &EventConsumer{
		rabbitmqURL: rabbitmqURL,
		queueName:   topology.embeddingStatusQueue,
		fileRepo:    fileRepo,
	}, nil
}

func (c *EventConsumer) getChannel() (*amqp.Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.channel != nil {
		return c.channel, nil
	}

	if c.conn == nil || c.conn.IsClosed() {
		slog.Info("Event consumer connecting to RabbitMQ", "url", c.rabbitmqURL)
		conn, err := amqp.Dial(c.rabbitmqURL)
		if err != nil {
			return nil, err
		}
		c.conn = conn
	}

	ch, err := c.conn.Channel()
	if err != nil {
		return nil, err
	}

	topology := configuredEventTopology()
	if err = declareEmbeddingStatusTopology(ch, topology); err != nil {
		_ = ch.Close()
		return nil, err
	}

	if err = ch.Qos(1, 0, false); err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("configure embedding status consumer QoS: %w", err)
	}

	c.channel = ch
	return c.channel, nil
}

func (c *EventConsumer) resetChannel() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.channel != nil {
		_ = c.channel.Close()
		c.channel = nil
	}
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

func (c *EventConsumer) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			slog.Info("Event consumer stopping (context cancelled)")
			return
		default:
		}

		ch, err := c.getChannel()
		if err != nil {
			slog.Error("event consumer: failed to establish connection/channel, retrying...", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}

		msgs, err := ch.Consume(c.queueName, "", false, false, false, false, nil)
		if err != nil {
			slog.Error("event consumer: failed to start consuming, resetting channel...", "error", err)
			c.resetChannel()
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}

		slog.Info("Event consumer started/resumed", "queue", c.queueName)
		c.consume(ctx, msgs)
	}
}

func (c *EventConsumer) consume(ctx context.Context, msgs <-chan amqp.Delivery) {
	for {
		select {
		case <-ctx.Done():
			return
		case d, ok := <-msgs:
			if !ok {
				slog.Warn("Event consumer channel closed, reconnecting...")
				c.resetChannel()
				return
			}
			if err := c.handle(ctx, d); err != nil {
				slog.Error("event consumer: failed to process event", "error", err)
				requeue := !errors.Is(err, errDiscardEvent)
				if nackErr := d.Nack(false, requeue); nackErr != nil {
					slog.Error("event consumer: failed to Nack event", "error", nackErr)
				}
				continue
			}
			if ackErr := d.Ack(false); ackErr != nil {
				slog.Error("event consumer: failed to Ack event", "error", ackErr)
			}
		}
	}
}

func (c *EventConsumer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var err error
	if c.channel != nil {
		err = c.channel.Close()
		c.channel = nil
	}
	if c.conn != nil {
		if connErr := c.conn.Close(); connErr != nil && err == nil {
			err = connErr
		}
		c.conn = nil
	}
	return err
}

func (c *EventConsumer) handle(ctx context.Context, d amqp.Delivery) error {
	var event contract.SystemEvent
	if err := json.Unmarshal(d.Body, &event); err != nil {
		slog.Error("event consumer: discarding malformed event", "error", err)
		return fmt.Errorf("%w: %v", errDiscardEvent, err)
	}

	status, ok := embeddingStatus(event.EventType)
	if !ok {
		return nil
	}

	fileID, _ := event.Payload["file_id"].(string)
	if fileID == "" {
		slog.Warn("event consumer: embedding event has no file_id", "event_type", event.EventType)
		return nil
	}

	record, err := c.fileRepo.GetByID(ctx, fileID)
	if err != nil {
		return fmt.Errorf("get file record %s: %w", fileID, err)
	}
	if record == nil {
		slog.Warn("event consumer: file record not found", "file_id", fileID)
		return nil
	}

	if err := c.fileRepo.UpdateEmbeddingStatus(ctx, fileID, status); err != nil {
		return fmt.Errorf("update file %s embedding status: %w", fileID, err)
	}
	return nil
}

func embeddingStatus(eventType string) (string, bool) {
	switch eventType {
	case "embedding.started":
		return "processing", true
	case "embedding.completed":
		return "completed", true
	case "embedding.failed":
		return "failed", true
	case "embedding.skipped":
		return "skipped", true
	default:
		return "", false
	}
}
