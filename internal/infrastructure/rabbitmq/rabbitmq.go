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

// RabbitMQPublisher implements file.EventPublisher interface.
type RabbitMQPublisher struct {
	conn     *amqp.Connection
	channel  *amqp.Channel
	mu       sync.RWMutex
	enabled  bool
	exchange string
	routeKey string
}

// NewRabbitMQPublisher constructs a new RabbitMQ EventPublisher.
func NewRabbitMQPublisher() (file.EventPublisher, error) {
	enabled := viper.GetBool("rabbitmq.enabled")
	exchange := viper.GetString("rabbitmq.exchange")
	routeKey := viper.GetString("rabbitmq.routing_key")

	if !enabled {
		slog.Info("RabbitMQ is disabled")
		return &RabbitMQPublisher{enabled: false}, nil
	}

	rabbitmqURL := viper.GetString("rabbitmq.url")
	slog.Info("Connecting to RabbitMQ", "url", rabbitmqURL)

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

	err = channel.ExchangeDeclare(
		exchange, // name
		"direct",  // type
		true,      // durable
		false,     // auto-deleted
		false,     // internal
		false,     // no-wait
		nil,       // arguments
	)
	if err != nil {
		_ = channel.Close()
		_ = conn.Close()
		slog.Error("failed to declare RabbitMQ exchange", "exchange", exchange, "error", err)
		return nil, err
	}

	slog.Info("RabbitMQ initialization completed", "exchange", exchange)

	return &RabbitMQPublisher{
		conn:     conn,
		channel:  channel,
		enabled:  true,
		exchange: exchange,
		routeKey: routeKey,
	}, nil
}

// PublishEvent publishes a SystemEvent to RabbitMQ.
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

	err = ch.PublishWithContext(
		pubCtx,
		p.exchange,
		p.routeKey,
		false,
		false,
		amqp.Publishing{
			ContentType: "application/json",
			Body:        body,
		},
	)
	if err != nil {
		slog.Error("failed to publish event to RabbitMQ", "event", eventType, "error", err)
		return
	}

	slog.Debug("Published event to RabbitMQ", "event", eventType)
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
