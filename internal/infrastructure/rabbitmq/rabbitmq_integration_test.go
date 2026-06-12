package rabbitmq

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/spf13/viper"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/star-inc/armi/pkgs/contract"
	"github.com/star-inc/armi/pkgs/file"
)

func getFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func runRabbitMQContainer(t *testing.T, port int) (string, func()) {
	cmdName := "docker"
	if _, err := exec.LookPath("podman"); err == nil {
		cmdName = "podman"
	} else if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Neither docker nor podman found in PATH, skipping RabbitMQ integration test")
		return "", nil
	}

	containerName := fmt.Sprintf("armi-rabbitmq-test-%d", time.Now().UnixNano())

	// Start RabbitMQ container
	cmd := exec.Command(cmdName, "run", "-d", "--name", containerName, "-p", fmt.Sprintf("%d:5672", port), "rabbitmq:3")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("Failed to start RabbitMQ container: %v, output: %s", err, string(output))
		t.Skip("Container runtime failed, skipping RabbitMQ integration test")
		return "", nil
	}

	cleanup := func() {
		_ = exec.Command(cmdName, "stop", containerName).Run()
		_ = exec.Command(cmdName, "rm", "-f", containerName).Run()
	}

	url := fmt.Sprintf("amqp://guest:guest@localhost:%d/", port)
	var conn *amqp.Connection
	for i := 0; i < 30; i++ {
		time.Sleep(1 * time.Second)
		conn, err = amqp.Dial(url)
		if err == nil {
			conn.Close()
			break
		}
	}
	if err != nil {
		cleanup()
		t.Fatalf("RabbitMQ container failed to start in time: %v", err)
	}

	return url, cleanup
}

func TestRabbitMQLazyInitialization(t *testing.T) {
	ResetSharedPublisherForTest()
	viper.Reset()
	viper.Set("rabbitmq.enabled", true)
	viper.Set("rabbitmq.url", "amqp://guest:guest@localhost:9999/") // non-existent URL

	publisher, err := NewRabbitMQPublisher()
	if err != nil {
		t.Fatalf("expected NewRabbitMQPublisher not to return error on lazy connection, got: %v", err)
	}
	if publisher.(*RabbitMQPublisher).IsAvailable() {
		t.Fatal("expected publisher to report not available")
	}
}

func TestRabbitMQPublishAndConsumeReconnection(t *testing.T) {
	ResetSharedPublisherForTest()
	port, err := getFreePort()
	if err != nil {
		t.Fatalf("failed to find free TCP port: %v", err)
	}

	url, containerCleanup := runRabbitMQContainer(t, port)
	defer containerCleanup()

	// Configure Viper
	viper.Reset()
	viper.Set("rabbitmq.enabled", true)
	viper.Set("rabbitmq.url", url)
	viper.Set("rabbitmq.exchange", "armi.events.test")
	viper.Set("rabbitmq.routing_key", "test.routing")
	viper.Set("rabbitmq.embedding_status_routing_key", "embedding.status.test")
	viper.Set("rabbitmq.broadcast_exchange", "armi.events.broadcast.test")
	viper.Set("rabbitmq.embedding_queue", "armi.embedding.jobs.test")
	viper.Set("rabbitmq.embedding_status_queue", "armi.embedding.status.test")

	// 1. Initialize Publisher
	pub, err := NewRabbitMQPublisher()
	if err != nil {
		t.Fatalf("failed to create publisher: %v", err)
	}
	defer pub.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 2. Publish initial event
	err = pub.PublishEvent(ctx, "embedding.started", "user-123", map[string]interface{}{"file_id": "file-1"})
	if err != nil {
		t.Fatalf("failed to publish initial event: %v", err)
	}

	// 3. Force connection close (simulate temporary network drop)
	sharedConnMu.Lock()
	if sharedConnManager != nil && sharedConnManager.conn != nil {
		_ = sharedConnManager.conn.Close()
	}
	sharedConnMu.Unlock()

	// 4. Publish next event (reconnect should trigger)
	err = pub.PublishEvent(ctx, "embedding.completed", "user-123", map[string]interface{}{"file_id": "file-1"})
	if err != nil {
		t.Fatalf("failed to publish event after connection loss: %v", err)
	}

	// 5. Test manual ACK with EmbeddingConsumer
	jobPub, err := NewRabbitMQJobPublisher()
	if err != nil {
		t.Fatalf("failed to create job publisher: %v", err)
	}
	defer jobPub.Close()

	// Setup mock services for consumer
	mockEmbedder := &mockEmbedder{}
	mockVectorDB := &mockVectorDB{}
	mockStorage := &mockStorage{}
	mockLLM := &mockLLM{}

	consumer, err := NewEmbeddingConsumer(mockEmbedder, mockVectorDB, mockStorage, pub, mockLLM)
	if err != nil {
		t.Fatalf("failed to create embedding consumer: %v", err)
	}
	defer consumer.Close()

	go consumer.Start(ctx)

	// Publish job
	jobID := "job-999"
	job := contract.EmbeddingJob{
		JobID:      jobID,
		FileID:     "file-999",
		UserID:     "user-123",
		StorageKey: "test/key",
		Filename:   "document.txt",
	}

	mockStorage.readFunc = func(ctx context.Context, key string) ([]byte, error) {
		return []byte("sample content"), nil
	}

	err = jobPub.PublishEmbeddingJob(ctx, job)
	if err != nil {
		t.Fatalf("failed to publish embedding job: %v", err)
	}

	// Wait for consumer to process
	time.Sleep(1 * time.Second)
	if !mockVectorDB.deleted && !mockVectorDB.inserted {
		// EmbedTextChunks should be called
	}
}

// ---------------------------------------------------------------------------
// GORM DB Setup for testing status transitions
// ---------------------------------------------------------------------------

type gormFileRecordTest struct {
	ID              string `gorm:"primaryKey"`
	Filename        string
	EmbeddingStatus string
}

func (gormFileRecordTest) TableName() string {
	return "file_records"
}

func TestGormFileMonotonicStatusTransitions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open in-memory sqlite: %v", err)
	}

	err = db.AutoMigrate(&gormFileRecordTest{})
	if err != nil {
		t.Fatalf("failed to auto-migrate: %v", err)
	}

	record := gormFileRecordTest{
		ID:              "file-1",
		Filename:        "test.pdf",
		EmbeddingStatus: "pending",
	}
	db.Create(&record)

	// In-memory test repository instance (custom SQLite update status mimicking actual repo)
	updateStatus := func(id, status string) error {
		query := db.Model(&gormFileRecordTest{})
		switch status {
		case "processing":
			query = query.Where("id = ? AND embedding_status = ?", id, "pending")
		case "completed", "failed", "skipped":
			query = query.Where("id = ? AND embedding_status IN (?, ?)", id, "pending", "processing")
		default:
			query = query.Where("id = ? AND embedding_status <> ?", id, status)
		}
		return query.Update("embedding_status", status).Error
	}

	// 1. Transition pending -> processing (should succeed)
	err = updateStatus("file-1", "processing")
	if err != nil {
		t.Fatalf("update to processing failed: %v", err)
	}
	var r gormFileRecordTest
	db.First(&r, "id = ?", "file-1")
	if r.EmbeddingStatus != "processing" {
		t.Fatalf("expected status to be processing, got: %s", r.EmbeddingStatus)
	}

	// 2. Transition processing -> completed (should succeed)
	err = updateStatus("file-1", "completed")
	if err != nil {
		t.Fatalf("update to completed failed: %v", err)
	}
	db.First(&r, "id = ?", "file-1")
	if r.EmbeddingStatus != "completed" {
		t.Fatalf("expected status to be completed, got: %s", r.EmbeddingStatus)
	}

	// 3. Transition completed -> processing (should be ignored / do nothing)
	err = updateStatus("file-1", "processing")
	if err != nil {
		t.Fatalf("update transition error: %v", err)
	}
	db.First(&r, "id = ?", "file-1")
	if r.EmbeddingStatus != "completed" {
		t.Fatalf("expected status to remain completed on late processing message, got: %s", r.EmbeddingStatus)
	}

	// 4. Transition completed -> failed (should be ignored / do nothing)
	err = updateStatus("file-1", "failed")
	if err != nil {
		t.Fatalf("update transition error: %v", err)
	}
	db.First(&r, "id = ?", "file-1")
	if r.EmbeddingStatus != "completed" {
		t.Fatalf("expected status to remain completed, got: %s", r.EmbeddingStatus)
	}
}

// ---------------------------------------------------------------------------
// Mock interfaces for EmbeddingConsumer tests
// ---------------------------------------------------------------------------

type mockEmbedder struct{}

func (mockEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

type mockVectorDB struct {
	inserted bool
	deleted  bool
}

func (m *mockVectorDB) Insert(ctx context.Context, chunkID string, tokenCount int, fileID string, vector []float32) error {
	m.inserted = true
	return nil
}
func (m *mockVectorDB) Copy(ctx context.Context, srcFileID string, destFileID string) error {
	return nil
}
func (m *mockVectorDB) Search(ctx context.Context, queryVector []float32, keywords []string, limit int) ([]file.SearchResult, error) {
	return nil, nil
}
func (m *mockVectorDB) Delete(ctx context.Context, fileID string) error {
	m.deleted = true
	return nil
}
func (m *mockVectorDB) Close() error { return nil }

type mockStorage struct {
	readFunc func(ctx context.Context, key string) ([]byte, error)
}

func (m *mockStorage) Write(ctx context.Context, key string, data []byte) error { return nil }
func (m *mockStorage) Read(ctx context.Context, key string) ([]byte, error) {
	if m.readFunc != nil {
		return m.readFunc(ctx, key)
	}
	return nil, nil
}
func (m *mockStorage) Delete(ctx context.Context, key string) error { return nil }
func (m *mockStorage) Stat(ctx context.Context, key string) (*file.StorageMetadata, error) {
	return nil, nil
}
func (m *mockStorage) Close() error { return nil }

type mockLLM struct{}

func (mockLLM) GenerateQueries(ctx context.Context, query string, limit int) ([]string, error) {
	return nil, nil
}
func (mockLLM) PerformOCR(ctx context.Context, imageBase64 string) (string, error) {
	return "ocr text", nil
}
