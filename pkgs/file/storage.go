package file

import "context"

// StorageMetadata wraps metadata queried from storage backend.
type StorageMetadata struct {
	ContentLength int64
	LastModified  string
}

// Storage interface abstracts OpenDAL storage backend interactions.
type Storage interface {
	Write(ctx context.Context, key string, data []byte) error
	Read(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	Stat(ctx context.Context, key string) (*StorageMetadata, error)
	Close() error
}
