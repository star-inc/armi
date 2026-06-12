package file

import (
	"context"

	"github.com/star-inc/armi/pkgs/contract"
)

// EventPublisher interface abstracts event publishing to RabbitMQ.
type EventPublisher interface {
	PublishEvent(ctx context.Context, eventType string, userID string, payload map[string]interface{}) error
	Close() error
}

// EmbeddingJobPublisher publishes embedding jobs to a RabbitMQ work queue.
// IsAvailable returns false when RabbitMQ is not connected; callers should
// fall back to synchronous embedding in that case.
type EmbeddingJobPublisher interface {
	PublishEmbeddingJob(ctx context.Context, job contract.EmbeddingJob) error
	IsAvailable() bool
	Close() error
}
