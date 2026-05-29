package extractor

import (
	"log/slog"

	"github.com/tmc/langchaingo/textsplitter"
)

// SplitText splits a string into chunks of size chunkSize (in characters)
// with chunkOverlap overlap using langchaingo's RecursiveCharacterTextSplitter.
func SplitText(text string, chunkSize, chunkOverlap int) []string {
	if chunkSize <= 0 || len(text) == 0 {
		return []string{text}
	}
	if chunkOverlap < 0 {
		chunkOverlap = 0
	}
	if chunkOverlap >= chunkSize {
		chunkOverlap = chunkSize / 2
	}

	splitter := textsplitter.NewRecursiveCharacter(
		textsplitter.WithChunkSize(chunkSize),
		textsplitter.WithChunkOverlap(chunkOverlap),
	)

	chunks, err := splitter.SplitText(text)
	if err != nil {
		slog.Error("langchaingo text splitter failed, falling back to manual split", "error", err)
		// Fallback to a simple character slice split if it fails
		runes := []rune(text)
		var manualChunks []string
		for i := 0; i < len(runes); i += chunkSize - chunkOverlap {
			end := i + chunkSize
			if end > len(runes) {
				end = len(runes)
			}
			manualChunks = append(manualChunks, string(runes[i:end]))
			if end == len(runes) {
				break
			}
		}
		return manualChunks
	}

	return chunks
}
