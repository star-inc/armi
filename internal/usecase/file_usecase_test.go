package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/star-inc/armi/pkgs/contract"
	"github.com/star-inc/armi/pkgs/file"
)

type fallbackRepo struct {
	status  string
	record  *file.FileRecord
	count   int64
	deleted bool
}

func (r *fallbackRepo) Create(context.Context, *file.FileRecord) error { return nil }
func (r *fallbackRepo) GetByID(context.Context, string) (*file.FileRecord, error) {
	return r.record, nil
}
func (r *fallbackRepo) GetByHash(context.Context, string) (*file.FileRecord, error) {
	return nil, nil
}
func (r *fallbackRepo) Update(context.Context, *file.FileRecord) error { return nil }
func (r *fallbackRepo) UpdateEmbeddingStatus(_ context.Context, _ string, status string) error {
	r.status = status
	return nil
}
func (r *fallbackRepo) List(context.Context, string, int, int) ([]*file.FileRecord, int64, error) {
	return nil, 0, nil
}
func (r *fallbackRepo) ListAccessible(context.Context, string, string, file.GroupPermission, int, int) ([]*file.FileRecord, int64, error) {
	return nil, 0, nil
}
func (r *fallbackRepo) ListByAuthorID(context.Context, string, string, int, int) ([]*file.FileRecord, int64, error) {
	return nil, 0, nil
}
func (r *fallbackRepo) GetGroupPermission(context.Context, string, string) (file.GroupPermission, bool, error) {
	return 0, false, nil
}
func (r *fallbackRepo) GetGroupIDsByFileID(context.Context, string) ([]string, error) {
	return nil, nil
}
func (r *fallbackRepo) ReplaceFileGroups(context.Context, string, []string) error { return nil }
func (r *fallbackRepo) Delete(context.Context, string) error {
	r.deleted = true
	return nil
}
func (r *fallbackRepo) CountByHash(context.Context, string) (int64, error) {
	return r.count, nil
}
func (r *fallbackRepo) GetByFilenameOrHash(context.Context, string, string, string) (*file.FileRecord, error) {
	return nil, nil
}
func (r *fallbackRepo) CreateWithOutbox(context.Context, *file.FileRecord, string) error { return nil }
func (r *fallbackRepo) GetPendingOutboxJobs(context.Context, int) ([]*file.OutboxJob, error) {
	return nil, nil
}
func (r *fallbackRepo) DeleteOutboxJob(context.Context, string) error { return nil }
func (r *fallbackRepo) DeleteOutboxJobByFileID(context.Context, string) error { return nil }
func (r *fallbackRepo) GetOrCreateHashRecord(context.Context, string) (int64, error) {
	return r.count, nil
}
func (r *fallbackRepo) DecrementHashRecord(context.Context, string) (int64, error) {
	return r.count, nil
}
func (r *fallbackRepo) CreateCleanupJob(context.Context, *file.CleanupJob) error { return nil }
func (r *fallbackRepo) GetPendingCleanupJobs(context.Context, int) ([]*file.CleanupJob, error) {
	return nil, nil
}
func (r *fallbackRepo) UpdateCleanupJob(context.Context, *file.CleanupJob) error { return nil }
func (r *fallbackRepo) DeleteCleanupJob(context.Context, string) error { return nil }
func (r *fallbackRepo) DeleteWithCleanup(context.Context, string, *file.CleanupJob) error {
	r.deleted = true
	return nil
}

type failingJobPublisher struct{}

func (failingJobPublisher) PublishEmbeddingJob(context.Context, contract.EmbeddingJob) error {
	return errors.New("RabbitMQ unavailable")
}
func (failingJobPublisher) IsAvailable() bool { return true }
func (failingJobPublisher) Close() error      { return nil }

type fallbackEmbedder struct{}

func (fallbackEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1}, nil
}

type fallbackVectorDB struct {
	inserted bool
	deleted  bool
}

func (v *fallbackVectorDB) Insert(context.Context, string, int, string, []float32) error {
	v.inserted = true
	return nil
}
func (v *fallbackVectorDB) Copy(context.Context, string, string) error { return nil }
func (v *fallbackVectorDB) Search(context.Context, []float32, []string, int) ([]file.SearchResult, error) {
	return nil, nil
}
func (v *fallbackVectorDB) Delete(context.Context, string) error {
	v.deleted = true
	return nil
}
func (v *fallbackVectorDB) Close() error { return nil }

type failingDeleteStorage struct {
	content []byte
}

func (s *failingDeleteStorage) Write(context.Context, string, []byte) error { return nil }
func (s *failingDeleteStorage) Read(context.Context, string) ([]byte, error) {
	return s.content, nil
}
func (s *failingDeleteStorage) Delete(context.Context, string) error {
	return errors.New("storage delete failed")
}
func (s *failingDeleteStorage) Stat(context.Context, string) (*file.StorageMetadata, error) {
	return nil, nil
}
func (s *failingDeleteStorage) Close() error { return nil }

type fallbackPublisher struct{}

func (fallbackPublisher) PublishEvent(context.Context, string, string, map[string]interface{}) error {
	return nil
}
func (fallbackPublisher) Close() error { return nil }

func TestDispatchEmbeddingJobFallsBackToContentInMemory(t *testing.T) {
	repo := &fallbackRepo{}
	vectorDB := &fallbackVectorDB{}
	uc := &FileUsecase{
		fileRepo:     repo,
		embedder:     fallbackEmbedder{},
		vectorDB:     vectorDB,
		publisher:    fallbackPublisher{},
		jobPublisher: failingJobPublisher{},
	}

	status, err := uc.dispatchEmbeddingJob(
		context.Background(),
		"file-id",
		"user-id",
		"abcdef",
		"document.txt",
		"text/plain",
		[]byte("content retained by upload"),
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if status != "completed" || repo.status != "completed" {
		t.Fatalf("expected completed fallback status, got response=%q persisted=%q", status, repo.status)
	}
	if !vectorDB.inserted {
		t.Fatal("expected synchronous fallback to insert a vector")
	}
}

func TestDeleteRegistersCleanupJob(t *testing.T) {
	repo := &fallbackRepo{
		count: 0,
		record: &file.FileRecord{
			ID:              "file-id",
			Filename:        "document.txt",
			Hash:            "abcdef",
			EmbeddingStatus: "completed",
		},
	}
	uc := &FileUsecase{
		fileRepo:  repo,
		publisher: fallbackPublisher{},
	}

	_, err := uc.Delete(context.Background(), "user-id", "file-id")
	if err != nil {
		t.Fatalf("expected Delete to register job without error, got: %v", err)
	}
	if !repo.deleted {
		t.Fatal("expected DeleteWithCleanup to delete the file record and register cleanup job")
	}
}
