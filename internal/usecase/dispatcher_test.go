package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/star-inc/armi/pkgs/contract"
	"github.com/star-inc/armi/pkgs/file"
)

type mockDispatcherRepo struct {
	file.FileRepository
	mu           sync.Mutex
	pendingJobs  []*file.OutboxJob
	pendingErr   error
	deletedJobs  map[string]bool
	deletedCount int
}

func (m *mockDispatcherRepo) GetPendingOutboxJobs(ctx context.Context, limit int) ([]*file.OutboxJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingErr != nil {
		return nil, m.pendingErr
	}
	return m.pendingJobs, nil
}

func (m *mockDispatcherRepo) DeleteOutboxJob(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletedJobs[id] = true
	m.deletedCount++
	return nil
}

type mockJobPublisher struct {
	mu           sync.Mutex
	isAvailable  bool
	publishedJob []contract.EmbeddingJob
	publishErr   error
}

func (m *mockJobPublisher) IsAvailable() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.isAvailable
}

func (m *mockJobPublisher) PublishEmbeddingJob(ctx context.Context, job contract.EmbeddingJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.publishErr != nil {
		return m.publishErr
	}
	m.publishedJob = append(m.publishedJob, job)
	return nil
}

func (m *mockJobPublisher) Close() error {
	return nil
}

func TestOutboxDispatcher_PublisherUnavailable(t *testing.T) {
	repo := &mockDispatcherRepo{
		pendingJobs: []*file.OutboxJob{
			{ID: "job-1", Payload: `{"job_id": "job-1", "file_id": "file-1"}`},
		},
		deletedJobs: make(map[string]bool),
	}
	pub := &mockJobPublisher{isAvailable: false}

	dispatcher := NewOutboxDispatcher(repo, pub)
	dispatcher.dispatchPendingJobs(context.Background())

	if len(pub.publishedJob) > 0 {
		t.Error("expected no jobs to be published when publisher is unavailable")
	}
	if repo.deletedJobs["job-1"] {
		t.Error("expected pending jobs not to be processed or deleted")
	}
}

func TestOutboxDispatcher_NilPublisher(t *testing.T) {
	repo := &mockDispatcherRepo{
		pendingJobs: []*file.OutboxJob{
			{ID: "job-1", Payload: `{"job_id": "job-1", "file_id": "file-1"}`},
		},
	}
	dispatcher := NewOutboxDispatcher(repo, nil)
	dispatcher.dispatchPendingJobs(context.Background())
	// Should not panic, should just return
}

func TestOutboxDispatcher_GetPendingJobsErr(t *testing.T) {
	repo := &mockDispatcherRepo{
		pendingErr:  errors.New("db error"),
		deletedJobs: make(map[string]bool),
	}
	pub := &mockJobPublisher{isAvailable: true}

	dispatcher := NewOutboxDispatcher(repo, pub)
	dispatcher.dispatchPendingJobs(context.Background())

	if len(pub.publishedJob) > 0 {
		t.Error("expected no jobs to be published on db error")
	}
}

func TestOutboxDispatcher_MalformedPayload(t *testing.T) {
	repo := &mockDispatcherRepo{
		pendingJobs: []*file.OutboxJob{
			{ID: "job-malformed", Payload: `{"job_id": "job-malformed", "file_id": `}, // invalid json
		},
		deletedJobs: make(map[string]bool),
	}
	pub := &mockJobPublisher{isAvailable: true}

	dispatcher := NewOutboxDispatcher(repo, pub)
	dispatcher.dispatchPendingJobs(context.Background())

	if len(pub.publishedJob) > 0 {
		t.Error("expected no jobs to be published")
	}
	if !repo.deletedJobs["job-malformed"] {
		t.Error("expected malformed job to be deleted from outbox table")
	}
}

func TestOutboxDispatcher_PublishError(t *testing.T) {
	repo := &mockDispatcherRepo{
		pendingJobs: []*file.OutboxJob{
			{ID: "job-1", Payload: `{"job_id": "job-1", "file_id": "file-1"}`},
		},
		deletedJobs: make(map[string]bool),
	}
	pub := &mockJobPublisher{
		isAvailable: true,
		publishErr:  errors.New("publish error"),
	}

	dispatcher := NewOutboxDispatcher(repo, pub)
	dispatcher.dispatchPendingJobs(context.Background())

	if len(pub.publishedJob) > 0 {
		t.Error("expected no jobs to be published successfully")
	}
	if repo.deletedJobs["job-1"] {
		t.Error("expected job NOT to be deleted from outbox on publish error")
	}
}

func TestOutboxDispatcher_Success(t *testing.T) {
	repo := &mockDispatcherRepo{
		pendingJobs: []*file.OutboxJob{
			{ID: "job-1", Payload: `{"job_id": "job-1", "file_id": "file-1"}`},
			{ID: "job-2", Payload: `{"job_id": "job-2", "file_id": "file-2"}`},
		},
		deletedJobs: make(map[string]bool),
	}
	pub := &mockJobPublisher{
		isAvailable: true,
	}

	dispatcher := NewOutboxDispatcher(repo, pub)
	dispatcher.dispatchPendingJobs(context.Background())

	if len(pub.publishedJob) != 2 {
		t.Fatalf("expected 2 jobs to be published, got %d", len(pub.publishedJob))
	}
	if pub.publishedJob[0].JobID != "job-1" || pub.publishedJob[1].JobID != "job-2" {
		t.Errorf("published jobs mismatch: %+v", pub.publishedJob)
	}
	if !repo.deletedJobs["job-1"] || !repo.deletedJobs["job-2"] {
		t.Error("expected successfully published jobs to be deleted from outbox")
	}
}

func TestOutboxDispatcher_StartStop(t *testing.T) {
	repo := &mockDispatcherRepo{
		deletedJobs: make(map[string]bool),
	}
	pub := &mockJobPublisher{isAvailable: true}
	dispatcher := NewOutboxDispatcher(repo, pub)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately to prevent blocking

	dispatcher.Start(ctx) // should return immediately
}
