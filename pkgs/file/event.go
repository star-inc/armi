package file

import "context"

// EventPublisher interface abstracts event publishing to RabbitMQ.
type EventPublisher interface {
	PublishEvent(ctx context.Context, eventType string, userID string, payload map[string]interface{})
	Close() error
}
