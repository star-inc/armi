package rabbitmq

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/star-inc/armi/internal/embedding"
	"github.com/star-inc/armi/pkgs/contract"
	"github.com/star-inc/armi/pkgs/file"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/spf13/viper"
)

// EmbeddingConsumer consumes EmbeddingJob messages from the work queue and
// performs text extraction, embedding generation, and vector DB insertion.
// Progress events are broadcast via the EventPublisher.
type EmbeddingConsumer struct {
	mu          sync.Mutex
	rabbitmqURL string
	queueName   string
	conn        *amqp.Connection
	channel     *amqp.Channel

	embedder  file.Embedder
	vectorDB  file.VectorDB
	storage   file.Storage
	publisher file.EventPublisher
	llm       file.LLM
}

// NewEmbeddingConsumer creates an EmbeddingConsumer.
// Returns nil (no error) when RabbitMQ is disabled — callers should check for nil.
func NewEmbeddingConsumer(
	embedder file.Embedder,
	vectorDB file.VectorDB,
	storage file.Storage,
	publisher file.EventPublisher,
	llm file.LLM,
) (*EmbeddingConsumer, error) {
	if !viper.GetBool("rabbitmq.enabled") {
		slog.Info("RabbitMQ is disabled, embedding consumer will not start")
		return nil, nil
	}

	queueName := viper.GetString("rabbitmq.embedding_queue")
	rabbitmqURL := viper.GetString("rabbitmq.url")

	return &EmbeddingConsumer{
		rabbitmqURL: rabbitmqURL,
		queueName:   queueName,
		embedder:    embedder,
		vectorDB:    vectorDB,
		storage:     storage,
		publisher:   publisher,
		llm:         llm,
	}, nil
}

func (c *EmbeddingConsumer) getChannel() (*amqp.Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.channel != nil {
		return c.channel, nil
	}

	if c.conn == nil || c.conn.IsClosed() {
		slog.Info("Embedding consumer connecting to RabbitMQ", "url", c.rabbitmqURL)
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

	// Declare same durable queue (idempotent)
	if _, err = ch.QueueDeclare(c.queueName, true, false, false, false, nil); err != nil {
		_ = ch.Close()
		return nil, err
	}

	// Fair dispatch: don't send a new job until consumer has ACK'd the previous one
	if err = ch.Qos(1, 0, false); err != nil {
		_ = ch.Close()
		return nil, err
	}

	c.channel = ch
	return c.channel, nil
}

func (c *EmbeddingConsumer) resetChannel() {
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

// Start begins consuming messages from the work queue.
// It blocks until ctx is cancelled; call it in a goroutine.
func (c *EmbeddingConsumer) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			slog.Info("Embedding consumer stopping (context cancelled)")
			return
		default:
		}

		ch, err := c.getChannel()
		if err != nil {
			slog.Error("embedding consumer: failed to establish connection/channel, retrying...", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}

		msgs, err := ch.Consume(
			c.queueName,
			"",    // consumer tag — auto-generated
			false, // auto-ack: we ack manually after processing
			false, // exclusive
			false, // no-local
			false, // no-wait
			nil,
		)
		if err != nil {
			slog.Error("embedding consumer: failed to start consuming, resetting channel...", "error", err)
			c.resetChannel()
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}

		slog.Info("Embedding consumer started/resumed", "queue", c.queueName)
		c.consume(ctx, msgs)
	}
}

func (c *EmbeddingConsumer) consume(ctx context.Context, msgs <-chan amqp.Delivery) {
	for {
		select {
		case <-ctx.Done():
			return
		case d, ok := <-msgs:
			if !ok {
				slog.Warn("Embedding consumer channel closed, reconnecting...")
				c.resetChannel()
				return
			}
			c.handle(ctx, d)
		}
	}
}

// Close shuts down the consumer's AMQP resources.
func (c *EmbeddingConsumer) Close() error {
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

// handle processes a single delivery.
func (c *EmbeddingConsumer) handle(ctx context.Context, d amqp.Delivery) {
	var job contract.EmbeddingJob
	if err := json.Unmarshal(d.Body, &job); err != nil {
		slog.Error("embedding consumer: failed to unmarshal job, discarding", "error", err)
		if nackErr := d.Nack(false, false); nackErr != nil {
			slog.Error("embedding consumer: failed to Nack malformed job", "error", nackErr)
		}
		return
	}

	slog.Info("embedding consumer: processing job", "job_id", job.JobID, "file_id", job.FileID)

	_ = c.publisher.PublishEvent(ctx, "embedding.started", job.UserID, map[string]interface{}{
		"job_id":  job.JobID,
		"file_id": job.FileID,
	})

	var (
		skipped    bool
		processErr error
	)

	if job.IsCopy {
		processErr = c.handleCopy(ctx, job)
	} else {
		skipped, processErr = c.handleEmbed(ctx, job)
	}

	if processErr != nil {
		slog.Error("embedding consumer: job failed", "job_id", job.JobID, "error", processErr)
		if err := c.publisher.PublishEvent(ctx, "embedding.failed", job.UserID, map[string]interface{}{
			"job_id":  job.JobID,
			"file_id": job.FileID,
			"error":   processErr.Error(),
		}); err != nil {
			slog.Error("embedding consumer: failed to publish failure status", "job_id", job.JobID, "error", err)
			if nackErr := d.Nack(false, true); nackErr != nil {
				slog.Error("embedding consumer: failed to Nack job after status publish failure", "job_id", job.JobID, "error", nackErr)
			}
			return
		}
		// Nack with requeue if not already redelivered to handle transient errors
		if nackErr := d.Nack(false, !d.Redelivered); nackErr != nil {
			slog.Error("embedding consumer: failed to Nack failed job", "job_id", job.JobID, "error", nackErr)
		}
		return
	}

	if skipped {
		if err := c.publisher.PublishEvent(ctx, "embedding.skipped", job.UserID, map[string]interface{}{
			"job_id":  job.JobID,
			"file_id": job.FileID,
			"reason":  "no extractable text",
		}); err != nil {
			slog.Error("embedding consumer: failed to publish skipped status", "job_id", job.JobID, "error", err)
			if nackErr := d.Nack(false, true); nackErr != nil {
				slog.Error("embedding consumer: failed to Nack skipped job", "job_id", job.JobID, "error", nackErr)
			}
			return
		}
		if ackErr := d.Ack(false); ackErr != nil {
			slog.Error("embedding consumer: failed to Ack skipped job", "job_id", job.JobID, "error", ackErr)
		}
		return
	}

	if err := c.publisher.PublishEvent(ctx, "embedding.completed", job.UserID, map[string]interface{}{
		"job_id":  job.JobID,
		"file_id": job.FileID,
	}); err != nil {
		slog.Error("embedding consumer: failed to publish completion status", "job_id", job.JobID, "error", err)
		if nackErr := d.Nack(false, true); nackErr != nil {
			slog.Error("embedding consumer: failed to Nack job after status publish failure", "job_id", job.JobID, "error", nackErr)
		}
		return
	}

	if ackErr := d.Ack(false); ackErr != nil {
		slog.Error("embedding consumer: failed to Ack completed job", "job_id", job.JobID, "error", ackErr)
	}
	slog.Info("embedding consumer: job completed", "job_id", job.JobID, "file_id", job.FileID)
}

// handleCopy copies vectors from SrcFileID to FileID.
func (c *EmbeddingConsumer) handleCopy(ctx context.Context, job contract.EmbeddingJob) error {
	return c.vectorDB.Copy(ctx, job.SrcFileID, job.FileID)
}

// handleEmbed extracts text, generates embedding, and inserts into vector DB.
func (c *EmbeddingConsumer) handleEmbed(ctx context.Context, job contract.EmbeddingJob) (bool, error) {
	// Read file content from storage
	data, err := c.storage.Read(ctx, job.StorageKey)
	if err != nil {
		return false, err
	}

	// Extract text with OCR fallback.
	text, err := embedding.ExtractTextWithOCR(ctx, data, job.Filename, c.llm)
	if err != nil {
		return false, err
	}
	if text == "" {
		slog.Info("embedding consumer: no text extracted, skipping embedding", "job_id", job.JobID)
		return true, nil
	}

	_ = c.publisher.PublishEvent(ctx, "embedding.text_extracted", job.UserID, map[string]interface{}{
		"job_id":      job.JobID,
		"file_id":     job.FileID,
		"text_length": len(text),
	})

	// Generate embedding and insert vectors.
	if err := embedding.EmbedTextChunks(ctx, job.FileID, text, c.embedder, c.vectorDB); err != nil {
		slog.Error("embedding failed (async)", "job_id", job.JobID, "file_id", job.FileID, "error", err)
		return false, err
	}

	return false, nil
}
