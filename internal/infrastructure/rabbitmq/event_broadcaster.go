package rabbitmq

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/star-inc/armi/pkgs/contract"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/spf13/viper"
)

// EventBroadcaster receives fanout events from RabbitMQ and forwards them to local subscribers.
type EventBroadcaster interface {
	Broadcast(event contract.SystemEvent)
}

// RabbitMQEventBroadcaster consumes the broadcast exchange and forwards events to an in-process hub.
type RabbitMQEventBroadcaster struct {
	conn      *amqp.Connection
	channel   *amqp.Channel
	mu        sync.Mutex
	queueName string
	exchange  string
	target    EventBroadcaster
}

// NewEventBroadcaster connects to the broadcast exchange and forwards messages into the given target.
func NewEventBroadcaster(target EventBroadcaster) (*RabbitMQEventBroadcaster, error) {
	if target == nil {
		return nil, nil
	}

	if !viper.GetBool("rabbitmq.enabled") {
		slog.Info("RabbitMQ is disabled, event broadcaster will not start")
		return nil, nil
	}

	exchange := viper.GetString("rabbitmq.broadcast_exchange")
	if exchange == "" {
		slog.Warn("rabbitmq.broadcast_exchange is empty, event broadcaster cannot subscribe")
		return nil, nil
	}

	rabbitmqURL := viper.GetString("rabbitmq.url")
	slog.Info("Connecting to RabbitMQ (event broadcaster)", "url", rabbitmqURL)

	conn, err := amqp.Dial(rabbitmqURL)
	if err != nil {
		slog.Error("failed to connect to RabbitMQ for event broadcaster", "error", err)
		return nil, err
	}

	channel, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		slog.Error("failed to open channel for event broadcaster", "error", err)
		return nil, err
	}

	if err = channel.ExchangeDeclare(exchange, "fanout", true, false, false, false, nil); err != nil {
		_ = channel.Close()
		_ = conn.Close()
		slog.Error("failed to declare RabbitMQ broadcast exchange", "exchange", exchange, "error", err)
		return nil, err
	}

	q, err := channel.QueueDeclare(
		"",
		false,
		true,
		true,
		false,
		nil,
	)
	if err != nil {
		_ = channel.Close()
		_ = conn.Close()
		slog.Error("failed to declare temporary event broadcaster queue", "error", err)
		return nil, err
	}

	if err = channel.QueueBind(q.Name, "", exchange, false, nil); err != nil {
		_ = channel.Close()
		_ = conn.Close()
		slog.Error("failed to bind temporary queue to broadcast exchange", "queue", q.Name, "exchange", exchange, "error", err)
		return nil, err
	}

	slog.Info("RabbitMQ event broadcaster initialized", "queue", q.Name, "exchange", exchange)

	return &RabbitMQEventBroadcaster{
		conn:      conn,
		channel:   channel,
		queueName: q.Name,
		exchange:  exchange,
		target:    target,
	}, nil
}

// Start begins consuming broadcast messages until the context is cancelled.
func (b *RabbitMQEventBroadcaster) Start(ctx context.Context) {
	b.mu.Lock()
	ch := b.channel
	b.mu.Unlock()

	msgs, err := ch.Consume(
		b.queueName,
		"",
		true,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		slog.Error("failed to start consuming broadcast exchange", "queue", b.queueName, "error", err)
		return
	}

	slog.Info("Event broadcaster started", "queue", b.queueName, "exchange", b.exchange)

	for {
		select {
		case <-ctx.Done():
			slog.Info("Event broadcaster stopping (context cancelled)")
			return
		case d, ok := <-msgs:
			if !ok {
				slog.Warn("Event broadcaster channel closed, stopping")
				return
			}
			var event contract.SystemEvent
			if err := json.Unmarshal(d.Body, &event); err != nil {
				slog.Error("event broadcaster: failed to unmarshal system event", "error", err)
				continue
			}
			b.target.Broadcast(event)
		}
	}
}

// Close gracefully closes the AMQP resources.
func (b *RabbitMQEventBroadcaster) Close() error {
	if b == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	var err error
	if b.channel != nil {
		err = b.channel.Close()
		b.channel = nil
	}
	if b.conn != nil {
		if connErr := b.conn.Close(); connErr != nil && err == nil {
			err = connErr
		}
		b.conn = nil
	}
	return err
}
