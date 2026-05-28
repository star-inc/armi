package http

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/rs/xid"
	"github.com/supersonictw/armi/internal/usecase"
	"github.com/supersonictw/armi/pkgs/file"
	"github.com/supersonictw/armi/pkgs/user"
)

// FileHandler handles HTTP endpoints for document file management.
type FileHandler struct {
	fileUsecase *usecase.FileUsecase
	publisher   file.EventPublisher
}

// NewFileHandler constructs a new FileHandler.
func NewFileHandler(fileUsecase *usecase.FileUsecase, publisher file.EventPublisher) *FileHandler {
	return &FileHandler{
		fileUsecase: fileUsecase,
		publisher:   publisher,
	}
}

// Upload handles multipart file upload, records progress, and invokes UseCase.
func (h *FileHandler) Upload(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	dbUser := val.(*user.User)

	multipartFile, fileHeader, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing file"})
		return
	}
	defer func() { _ = multipartFile.Close() }()

	transferID := xid.New().String()
	h.publisher.PublishEvent(c.Request.Context(), "file.upload_started", dbUser.ID, map[string]interface{}{
		"transfer_id": transferID,
		"filename":    fileHeader.Filename,
		"size":        fileHeader.Size,
	})

	var content bytes.Buffer
	buf := make([]byte, 512*1024)
	var bytesUploaded int64
	totalBytes := fileHeader.Size

	for {
		n, err := multipartFile.Read(buf)
		if n > 0 {
			content.Write(buf[:n])
			bytesUploaded += int64(n)
			percentage := float64(bytesUploaded) / float64(totalBytes) * 100.0

			h.publisher.PublishEvent(c.Request.Context(), "file.upload_progress", dbUser.ID, map[string]interface{}{
				"transfer_id":    transferID,
				"filename":       fileHeader.Filename,
				"bytes_uploaded": bytesUploaded,
				"total_bytes":    totalBytes,
				"percentage":     percentage,
			})
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			slog.Error("failed to read upload stream", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read upload file"})
			return
		}
	}

	resp, err := h.fileUsecase.Upload(
		c.Request.Context(),
		dbUser.ID,
		fileHeader.Filename,
		fileHeader.Header.Get("Content-Type"),
		content.Bytes(),
		transferID,
	)
	if err != nil {
		if err.Error() == "file conflict: identical file or filename already exists" {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// List lists all files owned by the user.
func (h *FileHandler) List(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	dbUser := val.(*user.User)

	files, err := h.fileUsecase.List(c.Request.Context(), dbUser.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	c.JSON(http.StatusOK, files)
}

// Download downloads a file by ID.
func (h *FileHandler) Download(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	dbUser := val.(*user.User)
	fileID := c.Param("id")

	data, filename, contentType, size, err := h.fileUsecase.Download(c.Request.Context(), dbUser.ID, fileID)
	if err != nil {
		if err.Error() == "file not found" {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		if err.Error() == "access denied" {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "download error"})
		return
	}

	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", contentType)
	c.Header("Content-Length", strconv.FormatInt(size, 10))
	c.Data(http.StatusOK, contentType, data)
}

// GetMetadata returns database and physical metadata of a file.
func (h *FileHandler) GetMetadata(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	dbUser := val.(*user.User)
	fileID := c.Param("id")

	record, opMetadata, err := h.fileUsecase.GetMetadata(c.Request.Context(), dbUser.ID, fileID)
	if err != nil {
		if err.Error() == "file not found" {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		if err.Error() == "access denied" {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "metadata error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"database_record":  record,
		"physical_storage": opMetadata,
	})
}

// Delete deletes a file.
func (h *FileHandler) Delete(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	dbUser := val.(*user.User)
	fileID := c.Param("id")

	physicalDeleted, err := h.fileUsecase.Delete(c.Request.Context(), dbUser.ID, fileID)
	if err != nil {
		if err.Error() == "file not found" {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		if err.Error() == "access denied" {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":          "file deleted successfully",
		"physical_deleted": physicalDeleted,
	})
}

// Search performs a semantic search over files.
func (h *FileHandler) Search(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	dbUser := val.(*user.User)

	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query parameter 'q' is required"})
		return
	}

	limit := 5
	if limitStr := c.Query("limit"); limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 {
			limit = parsedLimit
		}
	}

	items, err := h.fileUsecase.Search(c.Request.Context(), dbUser.ID, query, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "search failed"})
		return
	}

	c.JSON(http.StatusOK, items)
}
