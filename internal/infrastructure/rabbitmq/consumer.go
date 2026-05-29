package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/spf13/viper"
	"github.com/supersonictw/armi/internal/extractor"
	"github.com/supersonictw/armi/pkgs/contract"
	"github.com/supersonictw/armi/pkgs/file"
)

// EmbeddingConsumer consumes EmbeddingJob messages from the work queue and
// performs text extraction, embedding generation, and vector DB insertion.
// Progress events are broadcast via the EventPublisher.
type EmbeddingConsumer struct {
	conn      *amqp.Connection
	channel   *amqp.Channel
	mu        sync.Mutex
	queueName string

	embedder  file.Embedder
	vectorDB  file.VectorDB
	storage   file.Storage
	publisher file.EventPublisher
	llm       file.LLM
}

// NewEmbeddingConsumer creates an EmbeddingConsumer connected to RabbitMQ.
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
	slog.Info("Connecting to RabbitMQ (embedding consumer)", "url", rabbitmqURL)

	conn, err := amqp.Dial(rabbitmqURL)
	if err != nil {
		slog.Error("failed to connect to RabbitMQ for embedding consumer", "error", err)
		return nil, err
	}

	channel, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		slog.Error("failed to open channel for embedding consumer", "error", err)
		return nil, err
	}

	// Declare same durable queue (idempotent)
	if _, err = channel.QueueDeclare(queueName, true, false, false, false, nil); err != nil {
		_ = channel.Close()
		_ = conn.Close()
		slog.Error("failed to declare embedding queue in consumer", "queue", queueName, "error", err)
		return nil, err
	}

	// Fair dispatch: don't send a new job until consumer has ACK'd the previous one
	if err = channel.Qos(1, 0, false); err != nil {
		_ = channel.Close()
		_ = conn.Close()
		slog.Error("failed to set QoS for embedding consumer", "error", err)
		return nil, err
	}

	slog.Info("RabbitMQ embedding consumer initialized", "queue", queueName)

	return &EmbeddingConsumer{
		conn:      conn,
		channel:   channel,
		queueName: queueName,
		embedder:  embedder,
		vectorDB:  vectorDB,
		storage:   storage,
		publisher: publisher,
		llm:       llm,
	}, nil
}

// Start begins consuming messages from the work queue.
// It blocks until ctx is cancelled; call it in a goroutine.
func (c *EmbeddingConsumer) Start(ctx context.Context) {
	c.mu.Lock()
	ch := c.channel
	c.mu.Unlock()

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
		slog.Error("failed to start consuming embedding queue", "queue", c.queueName, "error", err)
		return
	}

	slog.Info("Embedding consumer started", "queue", c.queueName)

	for {
		select {
		case <-ctx.Done():
			slog.Info("Embedding consumer stopping (context cancelled)")
			return
		case d, ok := <-msgs:
			if !ok {
				slog.Warn("Embedding consumer channel closed, stopping")
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

	c.publisher.PublishEvent(ctx, "embedding.started", job.UserID, map[string]interface{}{
		"job_id":  job.JobID,
		"file_id": job.FileID,
	})

	var processErr error

	if job.IsCopy {
		processErr = c.handleCopy(ctx, job)
	} else {
		processErr = c.handleEmbed(ctx, job)
	}

	if processErr != nil {
		slog.Error("embedding consumer: job failed", "job_id", job.JobID, "error", processErr)
		c.publisher.PublishEvent(ctx, "embedding.failed", job.UserID, map[string]interface{}{
			"job_id":  job.JobID,
			"file_id": job.FileID,
			"error":   processErr.Error(),
		})
		// Nack without requeue to avoid poison-pill loops; move to DLQ if configured
		if nackErr := d.Nack(false, false); nackErr != nil {
			slog.Error("embedding consumer: failed to Nack failed job", "job_id", job.JobID, "error", nackErr)
		}
		return
	}

	c.publisher.PublishEvent(ctx, "embedding.completed", job.UserID, map[string]interface{}{
		"job_id":  job.JobID,
		"file_id": job.FileID,
	})

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
func (c *EmbeddingConsumer) handleEmbed(ctx context.Context, job contract.EmbeddingJob) error {
	// Read file content from storage
	data, err := c.storage.Read(ctx, job.StorageKey)
	if err != nil {
		return err
	}

	// Extract text
	text, err := extractor.ExtractText(data, job.Filename)
	if err != nil {
		// Text extraction failure is a system-level error (e.g. corrupted file, unsupported format).
		// Return the error so handle() publishes embedding.failed as an alert.
		return fmt.Errorf("text extraction failed: %w", err)
	}
	if text == "" && c.llm != nil {
		lowerFilename := strings.ToLower(job.Filename)
		if strings.HasSuffix(lowerFilename, ".pdf") {
			slog.Info("embedding consumer: extracted text is empty, trying OCR on PDF pages", "job_id", job.JobID, "filename", job.Filename)
			ocrText, ocrErr := extractor.PerformOCRForPDF(ctx, data, c.llm)
			if ocrErr == nil && ocrText != "" {
				text = ocrText
				slog.Info("embedding consumer: successfully extracted text via PDF OCR", "job_id", job.JobID, "filename", job.Filename, "text_len", len(text))
			} else if ocrErr != nil {
				slog.Warn("embedding consumer: PDF OCR fallback failed", "job_id", job.JobID, "filename", job.Filename, "error", ocrErr)
			}
		} else if strings.HasSuffix(lowerFilename, ".pptx") || strings.HasSuffix(lowerFilename, ".ppt") {
			slog.Info("embedding consumer: extracted text is empty, trying OCR on PPTX embedded images", "job_id", job.JobID, "filename", job.Filename)
			ocrText, ocrErr := extractor.PerformOCRForPPTX(ctx, data, c.llm)
			if ocrErr == nil && ocrText != "" {
				text = ocrText
				slog.Info("embedding consumer: successfully extracted text via PPTX OCR", "job_id", job.JobID, "filename", job.Filename, "text_len", len(text))
			} else if ocrErr != nil {
				slog.Warn("embedding consumer: PPTX OCR fallback failed", "job_id", job.JobID, "filename", job.Filename, "error", ocrErr)
			}
		}
	}
	if text == "" {
		slog.Info("embedding consumer: no text extracted, skipping embedding", "job_id", job.JobID)
		c.publisher.PublishEvent(ctx, "embedding.skipped", job.UserID, map[string]interface{}{
			"job_id":  job.JobID,
			"file_id": job.FileID,
			"reason":  "no extractable text",
		})
		return nil
	}

	c.publisher.PublishEvent(ctx, "embedding.text_extracted", job.UserID, map[string]interface{}{
		"job_id":      job.JobID,
		"file_id":     job.FileID,
		"text_length": len(text),
	})

	// Generate embedding
	chunkSize := viper.GetInt("chunk.size")
	if chunkSize <= 0 {
		chunkSize = 1000
	}
	chunkOverlap := viper.GetInt("chunk.overlap")
	if chunkOverlap < 0 {
		chunkOverlap = 200
	}

	chunks := extractor.SplitText(text, chunkSize, chunkOverlap)
	for i, chunk := range chunks {
		embeddingVal, err := c.embedder.Embed(ctx, chunk)
		if err != nil {
			slog.Error("embedding provider error (async)", "job_id", job.JobID, "file_id", job.FileID, "chunk_index", i, "error", err)
			return err
		}

		// Insert into vector DB — use a background context so the job finishes
		// even if the original request context has been cancelled.
		insertCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := c.vectorDB.Insert(insertCtx, job.FileID, i, chunk, embeddingVal); err != nil {
			return err
		}
	}

	return nil
}
