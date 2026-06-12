package rabbitmq

import (
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/spf13/viper"
)

type eventTopology struct {
	exchange             string
	routingKey           string
	embeddingStatusKey   string
	broadcastExchange    string
	embeddingStatusQueue string
}

func configuredEventTopology() eventTopology {
	return eventTopology{
		exchange:             viper.GetString("rabbitmq.exchange"),
		routingKey:           viper.GetString("rabbitmq.routing_key"),
		embeddingStatusKey:   viper.GetString("rabbitmq.embedding_status_routing_key"),
		broadcastExchange:    viper.GetString("rabbitmq.broadcast_exchange"),
		embeddingStatusQueue: viper.GetString("rabbitmq.embedding_status_queue"),
	}
}

func declareEmbeddingStatusTopology(channel *amqp.Channel, topology eventTopology) error {
	if topology.exchange == "" || topology.embeddingStatusKey == "" || topology.embeddingStatusQueue == "" {
		return fmt.Errorf("embedding status topology is incomplete")
	}
	if err := channel.ExchangeDeclare(topology.exchange, "direct", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare event exchange: %w", err)
	}
	q, err := channel.QueueDeclare(topology.embeddingStatusQueue, true, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("declare durable embedding status queue: %w", err)
	}
	if err := channel.QueueBind(q.Name, topology.embeddingStatusKey, topology.exchange, false, nil); err != nil {
		return fmt.Errorf("bind durable embedding status queue: %w", err)
	}
	return nil
}
