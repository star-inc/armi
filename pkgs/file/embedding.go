package file

import "context"

// Embedder interface abstracts embedding generation.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}
