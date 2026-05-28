package file

import "context"

// LLM abstracts chat completion capabilities for Query Expansion.
type LLM interface {
	GenerateQueries(ctx context.Context, query string, num int) ([]string, error)
}
