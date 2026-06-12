package file

import "context"

// FileRepository defines the database persistence contract for FileRecords.
type FileRepository interface {
	Create(ctx context.Context, f *FileRecord) error
	GetByID(ctx context.Context, id string) (*FileRecord, error)
	GetByHash(ctx context.Context, hash string) (*FileRecord, error)
	Update(ctx context.Context, f *FileRecord) error
	UpdateEmbeddingStatus(ctx context.Context, id string, status string) error
	List(ctx context.Context, tag string, limit int, offset int) ([]*FileRecord, int64, error)
	ListAccessible(ctx context.Context, userID string, tag string, required GroupPermission, limit int, offset int) ([]*FileRecord, int64, error)
	ListByAuthorID(ctx context.Context, authorID string, tag string, limit int, offset int) ([]*FileRecord, int64, error)
	GetGroupPermission(ctx context.Context, userID string, groupID string) (GroupPermission, bool, error)
	GetGroupIDsByFileID(ctx context.Context, fileID string) ([]string, error)
	ReplaceFileGroups(ctx context.Context, fileID string, groupIDs []string) error
	Delete(ctx context.Context, id string) error
	CountByHash(ctx context.Context, hash string) (int64, error)
	GetByFilenameOrHash(ctx context.Context, authorID string, filename string, hash string) (*FileRecord, error)

	CreateWithOutbox(ctx context.Context, f *FileRecord, payload string) error
	GetPendingOutboxJobs(ctx context.Context, limit int) ([]*OutboxJob, error)
	DeleteOutboxJob(ctx context.Context, id string) error
	DeleteOutboxJobByFileID(ctx context.Context, fileID string) error
	GetOrCreateHashRecord(ctx context.Context, hash string) (int64, error)
	DecrementHashRecord(ctx context.Context, hash string) (int64, error)
	CreateCleanupJob(ctx context.Context, job *CleanupJob) error
	GetPendingCleanupJobs(ctx context.Context, limit int) ([]*CleanupJob, error)
	UpdateCleanupJob(ctx context.Context, job *CleanupJob) error
	DeleteCleanupJob(ctx context.Context, id string) error
	DeleteWithCleanup(ctx context.Context, id string, job *CleanupJob) error
}
