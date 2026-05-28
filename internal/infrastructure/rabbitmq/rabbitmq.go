package rabbitmq

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/xid"
	"github.com/spf13/viper"
	"github.com/supersonictw/armi/pkgs/contract"
	"github.com/supersonictw/armi/pkgs/file"
)

// ---------------------------------------------------------------------------
// RabbitMQPublisher — broadcasts SystemEvents to armi.events (direct) and
// embedding progress to armi.events.broadcast (fanout).
// ---------------------------------------------------------------------------

// RabbitMQPublisher implements file.EventPublisher interface.
type RabbitMQPublisher struct {
	conn              *amqp.Connection
	channel           *amqp.Channel
	mu                sync.RWMutex
	enabled           bool
	exchange          string // direct exchange for legacy routing
	routeKey          string
	broadcastExchange string // fanout exchange for progress events
}

// NewRabbitMQPublisher constructs a new RabbitMQ EventPublisher.
func NewRabbitMQPublisher() (file.EventPublisher, error) {
	enabled := viper.GetBool("rabbitmq.enabled")
	exchange := viper.GetString("rabbitmq.exchange")
	routeKey := viper.GetString("rabbitmq.routing_key")
	broadcastExchange := viper.GetString("rabbitmq.broadcast_exchange")

	if !enabled {
		slog.Info("RabbitMQ is disabled")
		return &RabbitMQPublisher{enabled: false}, nil
	}

	rabbitmqURL := viper.GetString("rabbitmq.url")
	slog.Info("Connecting to RabbitMQ (publisher)", "url", rabbitmqURL)

	conn, err := amqp.Dial(rabbitmqURL)
	if err != nil {
		slog.Error("failed to connect to RabbitMQ", "error", err)
		return nil, err
	}

	channel, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		slog.Error("failed to open RabbitMQ channel", "error", err)
		return nil, err
	}

	// Declare direct exchange (legacy)
	if err = channel.ExchangeDeclare(exchange, "direct", true, false, false, false, nil); err != nil {
		_ = channel.Close()
		_ = conn.Close()
		slog.Error("failed to declare RabbitMQ exchange", "exchange", exchange, "error", err)
		return nil, err
	}

	// Declare fanout broadcast exchange for progress events
	if broadcastExchange != "" {
		if err = channel.ExchangeDeclare(broadcastExchange, "fanout", true, false, false, false, nil); err != nil {
			_ = channel.Close()
			_ = conn.Close()
			slog.Error("failed to declare RabbitMQ broadcast exchange", "exchange", broadcastExchange, "error", err)
			return nil, err
		}
	}

	slog.Info("RabbitMQ publisher initialized", "exchange", exchange, "broadcast_exchange", broadcastExchange)

	return &RabbitMQPublisher{
		conn:              conn,
		channel:           channel,
		enabled:           true,
		exchange:          exchange,
		routeKey:          routeKey,
		broadcastExchange: broadcastExchange,
	}, nil
}

// PublishEvent publishes a SystemEvent to RabbitMQ.
// Embedding progress events (prefix "embedding.") are additionally fanned out
// on the broadcast exchange so any subscriber can receive them.
func (p *RabbitMQPublisher) PublishEvent(ctx context.Context, eventType string, userID string, payload map[string]interface{}) {
	if !p.enabled {
		return
	}

	p.mu.RLock()
	ch := p.channel
	p.mu.RUnlock()

	if ch == nil {
		slog.Warn("RabbitMQ channel is not available, skipping event publish", "event", eventType)
		return
	}

	event := contract.SystemEvent{
		EventID:   xid.New().String(),
		EventType: eventType,
		UserID:    userID,
		Timestamp: time.Now().Format(time.RFC3339),
		Payload:   payload,
	}

	body, err := json.Marshal(event)
	if err != nil {
		slog.Error("failed to marshal event JSON", "event", eventType, "error", err)
		return
	}

	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	publishing := amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	}

	// Publish to direct exchange
	if err = ch.PublishWithContext(pubCtx, p.exchange, p.routeKey, false, false, publishing); err != nil {
		slog.Error("failed to publish event to RabbitMQ", "event", eventType, "error", err)
	} else {
		slog.Debug("Published event to RabbitMQ (direct)", "event", eventType)
	}

	// Additionally broadcast on fanout exchange
	if p.broadcastExchange != "" {
		if err = ch.PublishWithContext(pubCtx, p.broadcastExchange, "", false, false, publishing); err != nil {
			slog.Warn("failed to broadcast event on fanout exchange", "event", eventType, "error", err)
		} else {
			slog.Debug("Broadcast event on fanout exchange", "event", eventType)
		}
	}
}

// IsAvailable reports whether RabbitMQ is connected and usable.
func (p *RabbitMQPublisher) IsAvailable() bool {
	if !p.enabled {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.channel != nil && p.conn != nil && !p.conn.IsClosed()
}

// Close gracefully closes RabbitMQ connection and channel resources.
func (p *RabbitMQPublisher) Close() error {
	if !p.enabled {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	var err error
	if p.channel != nil {
		err = p.channel.Close()
		p.channel = nil
	}
	if p.conn != nil {
		connErr := p.conn.Close()
		if connErr != nil && err == nil {
			err = connErr
		}
		p.conn = nil
	}
	return err
}

// ---------------------------------------------------------------------------
// RabbitMQJobPublisher — dispatches EmbeddingJob messages to the work queue.
// ---------------------------------------------------------------------------

// RabbitMQJobPublisher implements file.EmbeddingJobPublisher.
type RabbitMQJobPublisher struct {
	conn      *amqp.Connection
	channel   *amqp.Channel
	mu        sync.RWMutex
	enabled   bool
	queueName string
}

// NewRabbitMQJobPublisher constructs a job publisher connected to the embedding work queue.
func NewRabbitMQJobPublisher() (file.EmbeddingJobPublisher, error) {
	enabled := viper.GetBool("rabbitmq.enabled")
	queueName := viper.GetString("rabbitmq.embedding_queue")

	if !enabled {
		slog.Info("RabbitMQ is disabled, embedding job publisher is a no-op")
		return &RabbitMQJobPublisher{enabled: false}, nil
	}

	rabbitmqURL := viper.GetString("rabbitmq.url")
	slog.Info("Connecting to RabbitMQ (job publisher)", "url", rabbitmqURL)

	conn, err := amqp.Dial(rabbitmqURL)
	if err != nil {
		slog.Error("failed to connect to RabbitMQ for job publisher", "error", err)
		return nil, err
	}

	channel, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		slog.Error("failed to open RabbitMQ channel for job publisher", "error", err)
		return nil, err
	}

	// Declare durable work queue
	if _, err = channel.QueueDeclare(queueName, true, false, false, false, nil); err != nil {
		_ = channel.Close()
		_ = conn.Close()
		slog.Error("failed to declare embedding work queue", "queue", queueName, "error", err)
		return nil, err
	}

	slog.Info("RabbitMQ job publisher initialized", "queue", queueName)

	return &RabbitMQJobPublisher{
		conn:      conn,
		channel:   channel,
		enabled:   true,
		queueName: queueName,
	}, nil
}

// PublishEmbeddingJob enqueues an EmbeddingJob to the work queue.
func (p *RabbitMQJobPublisher) PublishEmbeddingJob(ctx context.Context, job contract.EmbeddingJob) error {
	if !p.enabled {
		return nil
	}

	p.mu.RLock()
	ch := p.channel
	p.mu.RUnlock()

	if ch == nil {
		return errChannelUnavailable
	}

	body, err := json.Marshal(job)
	if err != nil {
		return err
	}

	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	err = ch.PublishWithContext(
		pubCtx,
		"",          // default exchange → route by queue name
		p.queueName, // routing key = queue name
		false,
		false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         body,
		},
	)
	if err != nil {
		slog.Error("failed to publish embedding job", "job_id", job.JobID, "error", err)
		return err
	}

	slog.Debug("Enqueued embedding job", "job_id", job.JobID, "file_id", job.FileID)
	return nil
}

// IsAvailable reports whether the job publisher can accept new jobs.
func (p *RabbitMQJobPublisher) IsAvailable() bool {
	if !p.enabled {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.channel != nil && p.conn != nil && !p.conn.IsClosed()
}

// Close gracefully closes the job publisher's connection.
func (p *RabbitMQJobPublisher) Close() error {
	if !p.enabled {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	var err error
	if p.channel != nil {
		err = p.channel.Close()
		p.channel = nil
	}
	if p.conn != nil {
		connErr := p.conn.Close()
		if connErr != nil && err == nil {
			err = connErr
		}
		p.conn = nil
	}
	return err
}
