package usecase

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/star-inc/armi/pkgs/contract"
	"github.com/star-inc/armi/pkgs/file"
	"github.com/spf13/viper"
)

type fallbackRepo struct {
	status            string
	record            *file.FileRecord
	count             int64
	deleted           bool
	accessibleFileIDs []string
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
func (r *fallbackRepo) GetAccessibleFileIDs(context.Context, string, file.GroupPermission) ([]string, error) {
	return r.accessibleFileIDs, nil
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
func (v *fallbackVectorDB) Search(context.Context, []float32, []string, []string, int) ([]file.SearchResult, error) {
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

func TestNewFileUsecaseGseConfig(t *testing.T) {
	// Backup original config values
	origEmbed := viper.Get("gse.dict_embed")
	origPaths := viper.Get("gse.dict_paths")
	defer func() {
		viper.Set("gse.dict_embed", origEmbed)
		viper.Set("gse.dict_paths", origPaths)
	}()

	// Create temp dictionary file
	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "custom_dict.txt")
	content := []byte("自定義分詞 100 n\n")
	if err := os.WriteFile(tempFile, content, 0644); err != nil {
		t.Fatalf("failed to write temp dict file: %v", err)
	}

	// Set config values
	viper.Set("gse.dict_embed", "")
	viper.Set("gse.dict_paths", []string{tempFile})

	// Initialize FileUsecase
	uc := NewFileUsecase(nil, nil, nil, nil, nil, nil, nil, nil)
	if uc.segmenter == nil {
		t.Fatal("expected segmenter to be initialized, got nil")
	}

	words := uc.segmenter.Cut("測試自定義分詞功能", true)
	found := false
	for _, w := range words {
		if w == "自定義分詞" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected custom word '自定義分詞' to be segmented, got words: %v", words)
	}
}

type mockReranker struct {
	called bool
	query  string
	docs   []string
	result []file.RerankResult
}

func (m *mockReranker) Rerank(ctx context.Context, query string, documents []string) ([]file.RerankResult, error) {
	m.called = true
	m.query = query
	m.docs = documents
	return m.result, nil
}

type mockSearchVectorDB struct {
	fallbackVectorDB
	mu                sync.Mutex
	lastSearchFileIDs []string
}

func (m *mockSearchVectorDB) Search(ctx context.Context, queryVector []float32, keywords []string, fileIDs []string, limit int) ([]file.SearchResult, error) {
	m.mu.Lock()
	m.lastSearchFileIDs = fileIDs
	m.mu.Unlock()
	return []file.SearchResult{
		{FileID: "file-1", ChunkID: "chunk-1", Text: "text-1", Distance: 0.1},
		{FileID: "file-1", ChunkID: "chunk-2", Text: "text-2", Distance: 0.8},
	}, nil
}

func TestSearchRerank(t *testing.T) {
	origEnabled := viper.Get("rerank.enabled")
	origLimit := viper.Get("rerank.query_limit")
	defer func() {
		viper.Set("rerank.enabled", origEnabled)
		viper.Set("rerank.query_limit", origLimit)
	}()

	viper.Set("rerank.enabled", true)
	viper.Set("rerank.query_limit", 5)

	repo := &fallbackRepo{
		record: &file.FileRecord{
			ID:              "file-1",
			Filename:        "doc1.txt",
			AuthorID:        "user-1",
			EmbeddingStatus: "completed",
		},
	}
	embedder := fallbackEmbedder{}
	vectorDB := &mockSearchVectorDB{}
	publisher := fallbackPublisher{}

	mRerank := &mockReranker{
		result: []file.RerankResult{
			{Index: 0, RelevanceScore: 0.1},
			{Index: 1, RelevanceScore: 0.9},
		},
	}

	uc := NewFileUsecase(repo, nil, embedder, vectorDB, nil, publisher, nil, mRerank)

	results, err := uc.Search(context.Background(), "user-1", "test query", 2, false, 0)
	if err != nil {
		t.Fatal(err)
	}

	if !mRerank.called {
		t.Fatal("expected reranker to be called")
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// Index 1 (chunk-2) has a relevance score of 0.9, so it should be first
	if results[0].Score != 0.9 {
		t.Fatalf("expected top result score to be 0.9, got %f", results[0].Score)
	}
	if results[0].ChunkID != "chunk-2" {
		t.Fatalf("expected top result chunk ID to be chunk-2, got %s", results[0].ChunkID)
	}
}

func TestSearchPreFiltering(t *testing.T) {
	// Enable RBAC
	origRbac := viper.Get("auth.rbac.enabled")
	defer func() {
		viper.Set("auth.rbac.enabled", origRbac)
	}()
	viper.Set("auth.rbac.enabled", true)

	repo := &fallbackRepo{
		accessibleFileIDs: []string{"file-1", "file-2"},
		record: &file.FileRecord{
			ID:              "file-1",
			Filename:        "doc1.txt",
			AuthorID:        "user-1",
			EmbeddingStatus: "completed",
		},
	}
	embedder := fallbackEmbedder{}
	vectorDB := &mockSearchVectorDB{}
	publisher := fallbackPublisher{}

	uc := NewFileUsecase(repo, nil, embedder, vectorDB, nil, publisher, nil, nil)

	results, err := uc.Search(context.Background(), "user-1", "test query", 2, false, 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	vectorDB.mu.Lock()
	searchFileIDs := vectorDB.lastSearchFileIDs
	vectorDB.mu.Unlock()

	if len(searchFileIDs) != 2 || searchFileIDs[0] != "file-1" || searchFileIDs[1] != "file-2" {
		t.Fatalf("expected search to be pre-filtered with [file-1 file-2], got %v", searchFileIDs)
	}
}

