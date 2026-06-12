package extractor

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/gabriel-vasile/mimetype"
	"github.com/lu4p/cat"
	"golang.org/x/net/html/charset"
	textunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// ExtractText takes file contents and the original filename, writes the content to
// a temporary file with the correct extension, uses lu4p/cat to extract the plain
// text, and then ensures the temporary file is deleted.
func ExtractText(content []byte, filename string) (string, error) {
	// Guard clause: empty content
	if len(content) == 0 {
		return "", nil
	}

	ext := strings.ToLower(filepath.Ext(filename))
	if ext == ".md" || ext == ".txt" {
		return decodeTextContent(content), nil
	}
	// Create a temp file that matches the original extension to help the cat parser identify it
	tempFile, err := os.CreateTemp("", "armi-upload-*"+ext)
	if err != nil {
		slog.Error("failed to create temporary file for text extraction", "error", err)
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}

	tempPath := tempFile.Name()
	defer func() {
		_ = tempFile.Close()
		if removeErr := os.Remove(tempPath); removeErr != nil {
			slog.Warn("failed to remove temporary file", "path", tempPath, "error", removeErr)
		}
	}()

	if _, err := tempFile.Write(content); err != nil {
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
	if looksLikeBinaryPayload(text) {
		// Treat binary-like extraction output as failure so caller can trigger OCR fallback.
		return "", nil
	}

	return text, nil
}

func decodeTextContent(content []byte) string {
	if len(content) == 0 {
		return ""
	}

	// UTF-8 with BOM
	if bytes.HasPrefix(content, []byte{0xEF, 0xBB, 0xBF}) {
		return string(content[3:])
	}

	// UTF-16 BOM
	if bytes.HasPrefix(content, []byte{0xFF, 0xFE}) || bytes.HasPrefix(content, []byte{0xFE, 0xFF}) {
		decoder := textunicode.UTF16(textunicode.BigEndian, textunicode.UseBOM).NewDecoder()
		if decoded, err := decodeWithTransformer(content, decoder); err == nil {
			return decoded
		}
	}

	if utf8.Valid(content) {
		return string(content)
	}

	// Use charset detector for legacy encodings (Big5/GBK/Shift-JIS, etc.).
	sample := content
	if len(sample) > 2048 {
		sample = sample[:2048]
	}
	if enc, _, certain := charset.DetermineEncoding(sample, "text/plain"); enc != nil && certain {
		if decoded, err := decodeWithTransformer(content, enc.NewDecoder()); err == nil && utf8.ValidString(decoded) {
			return decoded
		}
	}

	// Keep service available even when decode fails.
	return string(content)
}

func decodeWithTransformer(content []byte, t transform.Transformer) (string, error) {
	reader := transform.NewReader(bytes.NewReader(content), t)
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func looksLikeBinaryPayload(s string) bool {
	if s == "" {
		return false
	}

	mt := mimetype.Detect([]byte(s))
	if mt == nil {
		return false
	}
	mediaType := mt.String()
	if strings.HasPrefix(mediaType, "text/") {
		return false
	}
	switch mediaType {
	case "application/json", "application/xml", "application/javascript", "application/x-yaml":
		return false
	default:
		return true
	}
}
