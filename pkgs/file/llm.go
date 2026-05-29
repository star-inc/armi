package file

import "context"

// LLM abstracts chat completion capabilities for Query Expansion and OCR.
type LLM interface {
	GenerateQueries(ctx context.Context, query string, num int) ([]string, error)
	PerformOCR(ctx context.Context, imageBase64 string) (string, error)
}
