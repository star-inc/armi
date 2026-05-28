package file

import "context"

// SearchResult represents a matched vector point metadata.
type SearchResult struct {
	FileID   string
	Distance float32
}

// VectorDB interface abstracts vector database interactions (sqlite-vec/Qdrant).
type VectorDB interface {
	Insert(ctx context.Context, fileID string, embedding []float32) error
	Copy(ctx context.Context, srcFileID string, destFileID string) error
	Search(ctx context.Context, embedding []float32, limit int) ([]SearchResult, error)
	Delete(ctx context.Context, fileID string) error
	Close() error
}
