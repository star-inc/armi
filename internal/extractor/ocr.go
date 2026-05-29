package extractor

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/supersonictw/armi/pkgs/file"
)

// PerformOCRForPDF converts a PDF into page images using Ghostscript, and runs LLM-based OCR on each page.
func PerformOCRForPDF(ctx context.Context, pdfContent []byte, llm file.LLM) (string, error) {
	if len(pdfContent) == 0 || llm == nil {
		return "", nil
	}

	// Create temp directory for images
	tempDir, err := os.MkdirTemp("", "armi-ocr-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	// Write PDF content to a temp file
	pdfPath := filepath.Join(tempDir, "input.pdf")
	if err := os.WriteFile(pdfPath, pdfContent, 0600); err != nil {
		return "", fmt.Errorf("failed to write temp PDF: %w", err)
	}

	// Convert PDF pages to PNG using Ghostscript:
	// gs -dNOPAUSE -sDEVICE=png16m -r150 -sOutputFile=<tempDir>/page-%d.png <pdfPath> -c quit
	outputPattern := filepath.Join(tempDir, "page-%d.png")
	cmd := exec.CommandContext(ctx, "gs",
		"-dNOPAUSE",
		"-dBATCH",
		"-dSAFER",
		"-sDEVICE=png16m",
		"-r150",
		"-sOutputFile="+outputPattern,
		pdfPath,
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		slog.Error("Ghostscript conversion failed", "error", err, "output", string(output))
		return "", fmt.Errorf("failed to convert PDF pages to images: %w", err)
	}

	// Find all generated images
	files, err := os.ReadDir(tempDir)
	if err != nil {
		return "", fmt.Errorf("failed to read temp dir: %w", err)
	}

	var imageFiles []string
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "page-") && strings.HasSuffix(f.Name(), ".png") {
			imageFiles = append(imageFiles, filepath.Join(tempDir, f.Name()))
		}
	}

	// Sort image files numerically to process pages in order
	sortPageFiles(imageFiles)

	// Limit to first 20 pages to prevent API call explosion and high latency
	if len(imageFiles) > 20 {
		slog.Info("reached maximum page limit for PDF OCR, skipping remaining pages", "total", len(imageFiles))
		imageFiles = imageFiles[:20]
	}

	var extractedTexts []string
	for idx, imgPath := range imageFiles {
		imgBytes, err := os.ReadFile(imgPath)
		if err != nil {
			slog.Error("failed to read page image", "path", imgPath, "error", err)
			continue
		}

		base64Str := base64.StdEncoding.EncodeToString(imgBytes)
		slog.Info("Running OCR on PDF page", "page", idx+1, "total", len(imageFiles))
		
		text, err := llm.PerformOCR(ctx, base64Str)
		if err != nil {
			slog.Error("OCR failed on page", "page", idx+1, "error", err)
			continue
		}

		if text != "" {
			extractedTexts = append(extractedTexts, fmt.Sprintf("--- Page %d ---\n%s", idx+1, text))
		}
	}

	return strings.Join(extractedTexts, "\n\n"), nil
}

// PerformOCRForPPTX extracts all embedded images from a PPTX file and runs LLM-based OCR on them.
func PerformOCRForPPTX(ctx context.Context, pptxContent []byte, llm file.LLM) (string, error) {
	if len(pptxContent) == 0 || llm == nil {
		return "", nil
	}

	reader, err := zip.NewReader(bytes.NewReader(pptxContent), int64(len(pptxContent)))
	if err != nil {
		return "", fmt.Errorf("failed to open zip reader for PPTX: %w", err)
	}

	var extractedTexts []string
	imageCount := 0

	for _, f := range reader.File {
		// PPTX images are typically stored in ppt/media/
		if strings.HasPrefix(f.Name, "ppt/media/") {
			ext := strings.ToLower(filepath.Ext(f.Name))
			if ext == ".png" || ext == ".jpg" || ext == ".jpeg" {
				imageCount++
				
				// Limit to first 20 images to prevent API call explosion
				if imageCount > 20 {
					slog.Info("reached maximum image limit for PPTX OCR, skipping remaining images")
					break
				}

				rc, err := f.Open()
				if err != nil {
					slog.Error("failed to open media file in PPTX", "name", f.Name, "error", err)
					continue
				}

				imgBytes, err := io.ReadAll(io.LimitReader(rc, 32*1024*1024))
				_ = rc.Close()
				if err != nil {
					slog.Error("failed to read media file content in PPTX", "name", f.Name, "error", err)
					continue
				}

				base64Str := base64.StdEncoding.EncodeToString(imgBytes)
				slog.Info("Running OCR on PPTX embedded image", "name", f.Name)

				text, err := llm.PerformOCR(ctx, base64Str)
				if err != nil {
					slog.Error("OCR failed on PPTX image", "name", f.Name, "error", err)
					continue
				}

				if text != "" {
					extractedTexts = append(extractedTexts, fmt.Sprintf("--- Slide Image %d ---\n%s", imageCount, text))
				}
			}
		}
	}

	return strings.Join(extractedTexts, "\n\n"), nil
}

// sortPageFiles sorts image paths by their page number suffix
func sortPageFiles(files []string) {
	sort.Slice(files, func(i, j int) bool {
		numI := parsePageNumber(files[i])
		numJ := parsePageNumber(files[j])
		return numI < numJ
	})
}

func parsePageNumber(filename string) int {
	base := filepath.Base(filename)
	base = strings.TrimPrefix(base, "page-")
	base = strings.TrimSuffix(base, ".png")
	var num int
	fmt.Sscanf(base, "%d", &num)
	return num
}
