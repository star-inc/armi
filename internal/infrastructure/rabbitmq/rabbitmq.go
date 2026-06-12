package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/star-inc/armi/pkgs/contract"
	"github.com/star-inc/armi/pkgs/file"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/xid"
	"github.com/spf13/viper"
)

// ---------------------------------------------------------------------------
// ConnectionManager — manages a shared, lazily connected and auto-reconnecting AMQP connection.
// ---------------------------------------------------------------------------

type ConnectionManager struct {
	url  string
	conn *amqp.Connection
	mu   sync.Mutex
}

var (
	sharedConnManager *ConnectionManager
	sharedConnMu      sync.Mutex
)

func getConnectionManager(url string) *ConnectionManager {
	sharedConnMu.Lock()
	defer sharedConnMu.Unlock()
	if sharedConnManager == nil {
		sharedConnManager = &ConnectionManager{url: url}
	}
	return sharedConnManager
}

func (m *ConnectionManager) GetConnection() (*amqp.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.conn != nil && !m.conn.IsClosed() {
		return m.conn, nil
	}

	slog.Info("Connecting to RabbitMQ", "url", m.url)
	conn, err := amqp.Dial(m.url)
	if err != nil {
		return nil, err
	}
	m.conn = conn
	return m.conn, nil
}

func (m *ConnectionManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.conn != nil {
		err := m.conn.Close()
		m.conn = nil
		return err
	}
	return nil
}

func (m *ConnectionManager) IsOpen() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conn != nil && !m.conn.IsClosed()
}

// ---------------------------------------------------------------------------
// RabbitMQPublisher — broadcasts SystemEvents to armi.events (direct) and
// embedding progress to armi.events.broadcast (fanout).
// ---------------------------------------------------------------------------

// RabbitMQPublisher implements file.EventPublisher interface.
type RabbitMQPublisher struct {
	mu                sync.RWMutex
	enabled           bool
	exchange          string // direct exchange for legacy routing
	routeKey          string
	broadcastExchange string // fanout exchange for progress events

	connManager       *ConnectionManager
	channel           *amqp.Channel
}

var (
	sharedPublisher     *RabbitMQPublisher
	sharedPublisherOnce sync.Once
)

// NewRabbitMQPublisher constructs a new RabbitMQ EventPublisher.
func NewRabbitMQPublisher() (file.EventPublisher, error) {
	sharedPublisherOnce.Do(func() {
		enabled := viper.GetBool("rabbitmq.enabled")
		topology := configuredEventTopology()
		url := viper.GetString("rabbitmq.url")

		sharedPublisher = &RabbitMQPublisher{
			enabled:           enabled,
			exchange:          topology.exchange,
			routeKey:          topology.routingKey,
			broadcastExchange: topology.broadcastExchange,
			connManager:       getConnectionManager(url),
		}
	})
	return sharedPublisher, nil
}

func (p *RabbitMQPublisher) getChannel() (*amqp.Channel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.channel != nil {
		return p.channel, nil
	}

	conn, err := p.connManager.GetConnection()
	if err != nil {
		return nil, err
	}

	ch, err := conn.Channel()
	if err != nil {
		return nil, err
	}

	// Declare event exchange & status queue topology
	topology := configuredEventTopology()
	if err = declareEmbeddingStatusTopology(ch, topology); err != nil {
		_ = ch.Close()
		return nil, err
	}

	// Declare fanout broadcast exchange for progress events
	if p.broadcastExchange != "" {
		if err = ch.ExchangeDeclare(p.broadcastExchange, "fanout", true, false, false, false, nil); err != nil {
			_ = ch.Close()
			return nil, err
		}
	}

	if err = ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("enable RabbitMQ publisher confirms: %w", err)
	}

	p.channel = ch
	return p.channel, nil
}

func (p *RabbitMQPublisher) resetChannel() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.channel != nil {
		_ = p.channel.Close()
		p.channel = nil
	}
}

// PublishEvent publishes a SystemEvent to RabbitMQ with confirms & retries.
func (p *RabbitMQPublisher) PublishEvent(ctx context.Context, eventType string, userID string, payload map[string]interface{}) error {
	if p == nil || !p.enabled {
		return nil
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
		return err
	}

	publishing := amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		ch, err := p.getChannel()
		if err != nil {
			lastErr = err
			time.Sleep(100 * time.Millisecond * time.Duration(attempt))
			continue
		}

		pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)

		// 1. Publish to main exchange direct route
		err = ch.PublishWithContext(pubCtx, p.exchange, p.routeKey, false, false, publishing)
		if err != nil {
			cancel()
			slog.Error("failed to publish event to RabbitMQ, resetting channel", "event", eventType, "error", err)
			lastErr = err
			p.resetChannel()
			time.Sleep(100 * time.Millisecond * time.Duration(attempt))
			continue
		}

		// 2. Publish to status route with publisher confirmation if status event
		if _, isStatus := embeddingStatus(eventType); isStatus {
			statusKey := configuredEventTopology().embeddingStatusKey
			confirmation, confirmErr := ch.PublishWithDeferredConfirmWithContext(
				pubCtx, p.exchange, statusKey, false, false, publishing,
			)
			if confirmErr != nil {
				cancel()
				slog.Error("failed to publish embedding status event, resetting channel", "event", eventType, "error", confirmErr)
				lastErr = confirmErr
				p.resetChannel()
				time.Sleep(100 * time.Millisecond * time.Duration(attempt))
				continue
			}

			if confirmation == nil {
				cancel()
				lastErr = fmt.Errorf("embedding status publish confirmation is unavailable")
				p.resetChannel()
				time.Sleep(100 * time.Millisecond * time.Duration(attempt))
				continue
			}

			acknowledged, confirmErr := confirmation.WaitContext(pubCtx)
			if confirmErr != nil {
				cancel()
				slog.Error("embedding status publish was not confirmed", "event", eventType, "error", confirmErr)
				lastErr = confirmErr
				p.resetChannel()
				time.Sleep(100 * time.Millisecond * time.Duration(attempt))
				continue
			}

			if !acknowledged {
				cancel()
				lastErr = fmt.Errorf("embedding status publish was negatively acknowledged")
				p.resetChannel()
				time.Sleep(100 * time.Millisecond * time.Duration(attempt))
				continue
			}
		}

		// 3. Broadcast on fanout exchange
		if p.broadcastExchange != "" {
			_ = ch.PublishWithContext(pubCtx, p.broadcastExchange, "", false, false, publishing)
		}

		cancel()
		slog.Debug("Successfully published event to RabbitMQ", "event", eventType)
		return nil
	}

	return lastErr
}

// IsAvailable reports whether RabbitMQ is connected and usable.
func (p *RabbitMQPublisher) IsAvailable() bool {
	if p == nil || !p.enabled {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.channel != nil && p.connManager.IsOpen()
}

// Close gracefully closes RabbitMQ connection and channel resources.
func (p *RabbitMQPublisher) Close() error {
	if p == nil || !p.enabled {
		return nil
	}
	p.mu.Lock()
	if p.channel != nil {
		_ = p.channel.Close()
		p.channel = nil
	}
	p.mu.Unlock()
	return p.connManager.Close()
}

// ---------------------------------------------------------------------------
// RabbitMQJobPublisher — dispatches EmbeddingJob messages to the work queue.
// ---------------------------------------------------------------------------

// RabbitMQJobPublisher implements file.EmbeddingJobPublisher.
type RabbitMQJobPublisher struct {
	mu          sync.RWMutex
	enabled     bool
	queueName   string
	connManager *ConnectionManager
	channel     *amqp.Channel
}

var (
	sharedJobPublisher     *RabbitMQJobPublisher
	sharedJobPublisherOnce sync.Once
)

// NewRabbitMQJobPublisher constructs a job publisher connected to the embedding work queue.
func NewRabbitMQJobPublisher() (file.EmbeddingJobPublisher, error) {
	sharedJobPublisherOnce.Do(func() {
		enabled := viper.GetBool("rabbitmq.enabled")
		queueName := viper.GetString("rabbitmq.embedding_queue")
		url := viper.GetString("rabbitmq.url")

		sharedJobPublisher = &RabbitMQJobPublisher{
			enabled:     enabled,
			queueName:   queueName,
			connManager: getConnectionManager(url),
		}
	})
	return sharedJobPublisher, nil
}

func (p *RabbitMQJobPublisher) getChannel() (*amqp.Channel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.channel != nil {
		return p.channel, nil
	}

	conn, err := p.connManager.GetConnection()
	if err != nil {
		return nil, err
	}

	ch, err := conn.Channel()
	if err != nil {
		return nil, err
	}

	// Declare durable work queue
	if _, err = ch.QueueDeclare(p.queueName, true, false, false, false, nil); err != nil {
		_ = ch.Close()
		return nil, err
	}

	if err = ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("enable RabbitMQ job publisher confirms: %w", err)
	}

	p.channel = ch
	return p.channel, nil
}

func (p *RabbitMQJobPublisher) resetChannel() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.channel != nil {
		_ = p.channel.Close()
		p.channel = nil
	}
}

// PublishEmbeddingJob enqueues an EmbeddingJob to the work queue with confirms and retries.
func (p *RabbitMQJobPublisher) PublishEmbeddingJob(ctx context.Context, job contract.EmbeddingJob) error {
	if p == nil || !p.enabled {
		return nil
	}

	body, err := json.Marshal(job)
	if err != nil {
		return err
	}

	publishing := amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		ch, err := p.getChannel()
		if err != nil {
			lastErr = err
			time.Sleep(100 * time.Millisecond * time.Duration(attempt))
			continue
		}

		pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)

		confirmation, err := ch.PublishWithDeferredConfirmWithContext(
			pubCtx,
			"",          // default exchange → route by queue name
			p.queueName, // routing key = queue name
			false,
			false,
			publishing,
		)
		if err != nil {
			cancel()
			slog.Error("failed to publish embedding job, resetting channel", "job_id", job.JobID, "error", err)
			lastErr = err
			p.resetChannel()
			time.Sleep(100 * time.Millisecond * time.Duration(attempt))
			continue
		}

		if confirmation == nil {
			cancel()
			lastErr = fmt.Errorf("job publish confirmation is unavailable")
			p.resetChannel()
			time.Sleep(100 * time.Millisecond * time.Duration(attempt))
			continue
		}

		acknowledged, confirmErr := confirmation.WaitContext(pubCtx)
		if confirmErr != nil {
			cancel()
			slog.Error("job publish was not confirmed", "job_id", job.JobID, "error", confirmErr)
			lastErr = confirmErr
			p.resetChannel()
			time.Sleep(100 * time.Millisecond * time.Duration(attempt))
			continue
		}

		if !acknowledged {
			cancel()
			lastErr = fmt.Errorf("job publish was negatively acknowledged")
			p.resetChannel()
			time.Sleep(100 * time.Millisecond * time.Duration(attempt))
			continue
		}

		cancel()
		slog.Debug("Enqueued embedding job", "job_id", job.JobID, "file_id", job.FileID)
		return nil
	}

	return lastErr
}

// IsAvailable reports whether the job publisher can accept new jobs.
func (p *RabbitMQJobPublisher) IsAvailable() bool {
	if p == nil || !p.enabled {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.channel != nil && p.connManager.IsOpen()
}

// Close gracefully closes the job publisher's connection.
func (p *RabbitMQJobPublisher) Close() error {
	if p == nil || !p.enabled {
		return nil
	}
	p.mu.Lock()
	if p.channel != nil {
		_ = p.channel.Close()
		p.channel = nil
	}
	p.mu.Unlock()
	return p.connManager.Close()
}

// ResetSharedPublisherForTest resets the singletons in tests.
func ResetSharedPublisherForTest() {
	sharedPublisher = nil
	sharedPublisherOnce = sync.Once{}
	sharedJobPublisher = nil
	sharedJobPublisherOnce = sync.Once{}
	sharedConnManager = nil
	sharedConnMu = sync.Mutex{}
}
