package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/star-inc/armi/pkgs/file"
)

type mockCleanupRepo struct {
	file.FileRepository
	mu                 sync.Mutex
	pendingJobs        []*file.CleanupJob
	pendingJobsErr     error
	updatedJobs        map[string]*file.CleanupJob
	deletedCleanupJobs map[string]bool
	countByHash        map[string]int64
	countByHashErr     error
}

func (m *mockCleanupRepo) GetPendingCleanupJobs(ctx context.Context, limit int) ([]*file.CleanupJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingJobsErr != nil {
		return nil, m.pendingJobsErr
	}
	return m.pendingJobs, nil
}

func (m *mockCleanupRepo) UpdateCleanupJob(ctx context.Context, job *file.CleanupJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updatedJobs[job.ID] = job
	return nil
}

func (m *mockCleanupRepo) CountByHash(ctx context.Context, hash string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.countByHashErr != nil {
		return 0, m.countByHashErr
	}
	return m.countByHash[hash], nil
}

func (m *mockCleanupRepo) DeleteCleanupJob(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletedCleanupJobs[id] = true
	return nil
}

type mockCleanupVectorDB struct {
	file.VectorDB
	mu        sync.Mutex
	deletedID string
	deleteErr error
}

func (m *mockCleanupVectorDB) Delete(ctx context.Context, fileID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deletedID = fileID
	return nil
}

type mockCleanupStorage struct {
	file.Storage
	mu         sync.Mutex
	deletedKey string
	deleteErr  error
}

func (m *mockCleanupStorage) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deletedKey = key
	return nil
}

func TestCleanupWorkerProcessPendingCleanups_MaxRetries(t *testing.T) {
	repo := &mockCleanupRepo{
		pendingJobs: []*file.CleanupJob{
			{
				ID:         "job-dead",
				FileID:     "file-dead",
				RetryCount: 10,
				Status:     "pending",
			},
		},
		updatedJobs:        make(map[string]*file.CleanupJob),
		deletedCleanupJobs: make(map[string]bool),
	}

	worker := NewCleanupWorker(repo, nil, nil)
	worker.processPendingCleanups(context.Background())

	if repo.updatedJobs["job-dead"] == nil {
		t.Fatal("expected job to be updated")
	}
	if repo.updatedJobs["job-dead"].Status != "dead" {
		t.Errorf("expected status 'dead', got %s", repo.updatedJobs["job-dead"].Status)
	}
	if repo.deletedCleanupJobs["job-dead"] {
		t.Error("expected dead job to not be deleted from queue")
	}
}

func TestCleanupWorkerProcessPendingCleanups_VectorDBFailure(t *testing.T) {
	repo := &mockCleanupRepo{
		pendingJobs: []*file.CleanupJob{
			{
				ID:         "job-failed-vdb",
				FileID:     "file-failed-vdb",
				RetryCount: 2,
				Status:     "pending",
			},
		},
		updatedJobs:        make(map[string]*file.CleanupJob),
		deletedCleanupJobs: make(map[string]bool),
	}
	vdb := &mockCleanupVectorDB{
		deleteErr: errors.New("vector db delete error"),
	}

	worker := NewCleanupWorker(repo, vdb, nil)
	worker.processPendingCleanups(context.Background())

	if repo.updatedJobs["job-failed-vdb"] == nil {
		t.Fatal("expected job to be updated")
	}
	if repo.updatedJobs["job-failed-vdb"].Status != "failed" {
		t.Errorf("expected status 'failed', got %s", repo.updatedJobs["job-failed-vdb"].Status)
	}
	if repo.updatedJobs["job-failed-vdb"].RetryCount != 3 {
		t.Errorf("expected RetryCount to be 3, got %d", repo.updatedJobs["job-failed-vdb"].RetryCount)
	}
}

func TestCleanupWorkerProcessPendingCleanups_CountByHashFailure(t *testing.T) {
	repo := &mockCleanupRepo{
		pendingJobs: []*file.CleanupJob{
			{
				ID:         "job-failed-count",
				FileID:     "file-failed-count",
				Hash:       "hash-err",
				RetryCount: 1,
				Status:     "pending",
			},
		},
		countByHashErr:     errors.New("db count error"),
		updatedJobs:        make(map[string]*file.CleanupJob),
		deletedCleanupJobs: make(map[string]bool),
	}
	vdb := &mockCleanupVectorDB{}

	worker := NewCleanupWorker(repo, vdb, nil)
	worker.processPendingCleanups(context.Background())

	if repo.updatedJobs["job-failed-count"] == nil {
		t.Fatal("expected job to be updated")
	}
	if repo.updatedJobs["job-failed-count"].Status != "failed" {
		t.Errorf("expected status 'failed', got %s", repo.updatedJobs["job-failed-count"].Status)
	}
}

func TestCleanupWorkerProcessPendingCleanups_StorageFailure(t *testing.T) {
	repo := &mockCleanupRepo{
		pendingJobs: []*file.CleanupJob{
			{
				ID:             "job-failed-storage",
				FileID:         "file-failed-storage",
				Hash:           "hash-1",
				StorageKey:     "key-1",
				DeletePhysical: true,
				Status:         "pending",
			},
		},
		countByHash: map[string]int64{
			"hash-1": 0,
		},
		updatedJobs:        make(map[string]*file.CleanupJob),
		deletedCleanupJobs: make(map[string]bool),
	}
	vdb := &mockCleanupVectorDB{}
	storage := &mockCleanupStorage{
		deleteErr: errors.New("storage delete error"),
	}

	worker := NewCleanupWorker(repo, vdb, storage)
	worker.processPendingCleanups(context.Background())

	if repo.updatedJobs["job-failed-storage"] == nil {
		t.Fatal("expected job to be updated")
	}
	if repo.updatedJobs["job-failed-storage"].Status != "failed" {
		t.Errorf("expected status 'failed', got %s", repo.updatedJobs["job-failed-storage"].Status)
	}
}

func TestCleanupWorkerProcessPendingCleanups_Success(t *testing.T) {
	repo := &mockCleanupRepo{
		pendingJobs: []*file.CleanupJob{
			{
				ID:             "job-success-1",
				FileID:         "file-success-1",
				Hash:           "hash-shared",
				StorageKey:     "key-shared",
				DeletePhysical: true,
				Status:         "pending",
			},
			{
				ID:             "job-success-2",
				FileID:         "file-success-2",
				Hash:           "hash-unique",
				StorageKey:     "key-unique",
				DeletePhysical: true,
				Status:         "pending",
			},
		},
		countByHash: map[string]int64{
			"hash-shared": 2, // still referenced
			"hash-unique": 0, // no longer referenced
		},
		updatedJobs:        make(map[string]*file.CleanupJob),
		deletedCleanupJobs: make(map[string]bool),
	}
	vdb := &mockCleanupVectorDB{}
	storage := &mockCleanupStorage{}

	worker := NewCleanupWorker(repo, vdb, storage)
	worker.processPendingCleanups(context.Background())

	if vdb.deletedID != "file-success-2" {
		t.Errorf("expected deletedID in vector DB to be file-success-2, got %s", vdb.deletedID)
	}
	if storage.deletedKey != "key-unique" {
		t.Errorf("expected storage.deletedKey to be key-unique, got %s", storage.deletedKey)
	}
	if !repo.deletedCleanupJobs["job-success-1"] {
		t.Error("expected job-success-1 to be deleted from cleanup jobs queue")
	}
	if !repo.deletedCleanupJobs["job-success-2"] {
		t.Error("expected job-success-2 to be deleted from cleanup jobs queue")
	}
}

func TestCleanupWorker_StartStop(t *testing.T) {
	repo := &mockCleanupRepo{
		updatedJobs:        make(map[string]*file.CleanupJob),
		deletedCleanupJobs: make(map[string]bool),
	}
	worker := NewCleanupWorker(repo, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately to prevent blocking
	cancel()

	worker.Start(ctx) // should return immediately after context done
}
