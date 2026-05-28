package extractor

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/lu4p/cat"
)

// ExtractText takes file contents and the original filename, writes the content to
// a temporary file with the correct extension, uses lu4p/cat to extract the plain
// text, and then ensures the temporary file is deleted.
func ExtractText(content []byte, filename string) (string, error) {
	// Guard clause: empty content
	if len(content) == 0 {
		return "", nil
	}

	ext := filepath.Ext(filename)
	// Create a temp file that matches the original extension to help the cat parser identify it
	tempFile, err := os.CreateTemp("", "armi-upload-*"+ext)
	if err != nil {
		slog.Error("failed to create temporary file for text extraction", "error", err)
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}

	tempPath := tempFile.Name()
	defer func() {
		if removeErr := os.Remove(tempPath); removeErr != nil {
			slog.Warn("failed to remove temporary file", "path", tempPath, "error", removeErr)
		}
	}()

	if _, err := tempFile.Write(content); err != nil {
		_ = tempFile.Close()
		slog.Error("failed to write content to temporary file", "path", tempPath, "error", err)
		return "", fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := tempFile.Close(); err != nil {
		slog.Error("failed to close temporary file", "path", tempPath, "error", err)
		return "", fmt.Errorf("failed to close temp file: %w", err)
	}

	// Use lu4p/cat to extract text
	text, err := cat.File(tempPath)
	if err != nil {
		slog.Error("failed to extract text from file", "filename", filename, "error", err)
		return "", fmt.Errorf("failed to extract text: %w", err)
	}

	return text, nil
}
