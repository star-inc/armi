package file

import "context"

// Reranker abstracts the capability to rerank a list of candidate documents relative to a query.
type Reranker interface {
	Rerank(ctx context.Context, query string, documents []string) ([]RerankResult, error)
}

// RerankResult contains the index of the original document and its computed relevance score.
type RerankResult struct {
	Index          int
	RelevanceScore float32
}
