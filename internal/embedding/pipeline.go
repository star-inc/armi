package embedding

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/viper"
	"github.com/star-inc/armi/internal/extractor"
	"github.com/star-inc/armi/pkgs/file"
)

// ExtractTextWithOCR extracts plain text and performs OCR fallback for PDF/PPT when needed.
func ExtractTextWithOCR(ctx context.Context, content []byte, filename string, llm file.LLM) (string, error) {
	text, err := extractor.ExtractText(content, filename)
	if err != nil {
		return "", fmt.Errorf("text extraction failed: %w", err)
	}
	if text == "" && llm != nil {
		if !viper.GetBool("llm.ocr.enabled") {
			slog.Info("Extracted text is empty, OCR is disabled by configuration, skipping OCR fallback", "filename", filename)
			return "", nil
		}

		lowerFilename := strings.ToLower(filename)
		if strings.HasSuffix(lowerFilename, ".pdf") {
			slog.Info("Extracted text is empty, trying OCR on PDF pages", "filename", filename)
			maxPages := viper.GetInt("llm.ocr.max_pages")
			if maxPages <= 0 {
				maxPages = 20
			}
			ocrText, ocrErr := extractor.PerformOCRForPDF(ctx, content, llm, maxPages)
			if ocrErr == nil && ocrText != "" {
				text = ocrText
				slog.Info("Successfully extracted text via PDF OCR", "filename", filename, "text_len", len(text))
			} else if ocrErr != nil {
				slog.Warn("PDF OCR fallback failed", "filename", filename, "error", ocrErr)
			}
		} else if strings.HasSuffix(lowerFilename, ".pptx") || strings.HasSuffix(lowerFilename, ".ppt") {
			slog.Info("Extracted text is empty, trying OCR on PPTX embedded images", "filename", filename)
			maxImages := viper.GetInt("llm.ocr.max_images")
			if maxImages <= 0 {
				maxImages = 20
			}
			ocrText, ocrErr := extractor.PerformOCRForPPTX(ctx, content, llm, maxImages)
			if ocrErr == nil && ocrText != "" {
				text = ocrText
				slog.Info("Successfully extracted text via PPTX OCR", "filename", filename, "text_len", len(text))
			} else if ocrErr != nil {
				slog.Warn("PPTX OCR fallback failed", "filename", filename, "error", ocrErr)
			}
		}
	}
	return text, nil
}

// EmbedTextChunks splits text into chunks, generates vectors, and inserts them into vector DB.
func EmbedTextChunks(ctx context.Context, fileID string, text string, embedder file.Embedder, vectorDB file.VectorDB) error {
	chunkSize := viper.GetInt("chunk.size")
	if chunkSize <= 0 {
		chunkSize = 1000
	}
	chunkOverlap := viper.GetInt("chunk.overlap")
	if chunkOverlap < 0 {
		chunkOverlap = 200
	}

	chunks := extractor.SplitText(text, chunkSize, chunkOverlap)
	for i, chunk := range chunks {
		embeddingVal, err := embedder.Embed(ctx, chunk)
		if err != nil {
			return fmt.Errorf("embedding model error at chunk %d: %w", i, err)
		}
		if err := vectorDB.Insert(ctx, fileID, i, chunk, embeddingVal); err != nil {
			return fmt.Errorf("vector insert failed at chunk %d: %w", i, err)
		}
	}

	return nil
}
