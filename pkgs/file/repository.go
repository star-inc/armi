package file

import "context"

// FileRepository defines the database persistence contract for FileRecords.
type FileRepository interface {
	Create(ctx context.Context, f *FileRecord) error
	GetByID(ctx context.Context, id string) (*FileRecord, error)
	GetByHash(ctx context.Context, hash string) (*FileRecord, error)
	ListByOwnerID(ctx context.Context, ownerID string, tag string) ([]*FileRecord, error)
	Delete(ctx context.Context, id string) error
	CountByHash(ctx context.Context, hash string) (int64, error)
	CountByFilenameOrHash(ctx context.Context, ownerID string, filename string, hash string) (int64, error)
}
