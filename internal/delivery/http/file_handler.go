package http

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

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
// @Summary      Upload a document file
// @Description  Uploads a new file (PDF, Word, Excel, PPT, TXT, RTF) with upload progress reporting, SHA3-256 deduplication check, and embedding generation.
// @Tags         files
// @Accept       multipart/form-data
// @Produce      json
// @Param        file  formData  file  true  "The document file to upload"
// @Success      200   {object}  contract.FileResponse
// @Failure      400   {object}  map[string]string "Missing file or invalid request"
// @Failure      401   {object}  map[string]string "Unauthorized"
// @Failure      409   {object}  map[string]string "File conflict (duplicate filename or hash)"
// @Failure      500   {object}  map[string]string "Internal server error"
// @Security     BasicAuth
// @Router       /files [post]
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

	filename := filepath.Base(fileHeader.Filename)
	transferID := xid.New().String()
	h.publisher.PublishEvent(c.Request.Context(), "file.upload_started", dbUser.ID, map[string]interface{}{
		"transfer_id": transferID,
		"filename":    filename,
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
				"filename":       filename,
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

	tags := c.PostFormArray("tags")
	if len(tags) == 0 {
		if tagStr := c.PostForm("tags"); tagStr != "" {
			parts := strings.Split(tagStr, ",")
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					tags = append(tags, p)
				}
			}
		}
	} else if len(tags) == 1 && strings.Contains(tags[0], ",") {
		parts := strings.Split(tags[0], ",")
		tags = []string{}
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				tags = append(tags, p)
			}
		}
	}

	resp, err := h.fileUsecase.Upload(
		c.Request.Context(),
		dbUser.ID,
		filename,
		fileHeader.Header.Get("Content-Type"),
		content.Bytes(),
		transferID,
		tags,
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
// @Summary      List user files
// @Description  Retrieves list of all metadata records of files owned by the authenticated user.
// @Tags         files
// @Produce      json
// @Success      200   {array}   contract.FileResponse
// @Failure      401   {object}  map[string]string "Unauthorized"
// @Failure      500   {object}  map[string]string "Database error"
// @Security     BasicAuth
// @Router       /files [get]
func (h *FileHandler) List(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	dbUser := val.(*user.User)

	tag := c.Query("tag")
	files, err := h.fileUsecase.List(c.Request.Context(), dbUser.ID, tag)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	c.JSON(http.StatusOK, files)
}

// Download downloads a file by ID.
// @Summary      Download a file
// @Description  Downloads a file's raw content by its unique file record ID.
// @Tags         files
// @Produce      octet-stream
// @Param        id    path      string  true  "File ID"
// @Success      200   {file}    file "The downloaded file contents"
// @Failure      401   {object}  map[string]string "Unauthorized"
// @Failure      403   {object}  map[string]string "Forbidden (access denied)"
// @Failure      404   {object}  map[string]string "File not found"
// @Failure      500   {object}  map[string]string "Download error"
// @Security     BasicAuth
// @Router       /files/{id} [get]
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

	c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(filename))
	c.Header("Content-Type", contentType)
	c.Header("Content-Length", strconv.FormatInt(size, 10))
	c.Data(http.StatusOK, contentType, data)
}

// GetMetadata returns database and physical metadata of a file.
// @Summary      Get file metadata
// @Description  Fetches the database record metadata and physical storage (OpenDAL) stat information of the specified file.
// @Tags         files
// @Produce      json
// @Param        id    path      string  true  "File ID"
// @Success      200   {object}  map[string]interface{} "File metadata wrapper (database_record and physical_storage)"
// @Failure      401   {object}  map[string]string "Unauthorized"
// @Failure      403   {object}  map[string]string "Forbidden (access denied)"
// @Failure      404   {object}  map[string]string "File not found"
// @Failure      500   {object}  map[string]string "Metadata error"
// @Security     BasicAuth
// @Router       /files/{id}/metadata [get]
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
// @Summary      Delete a file
// @Description  Deletes the file record from database and vector database. If there are no other references, it deletes the physical file.
// @Tags         files
// @Produce      json
// @Param        id    path      string  true  "File ID"
// @Success      200   {object}  map[string]interface{} "Message and physical_deleted status"
// @Failure      401   {object}  map[string]string "Unauthorized"
// @Failure      403   {object}  map[string]string "Forbidden (access denied)"
// @Failure      404   {object}  map[string]string "File not found"
// @Failure      500   {object}  map[string]string "Delete error"
// @Security     BasicAuth
// @Router       /files/{id} [delete]
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
// @Summary      Search files semantically
// @Description  Generates an embedding query and performs vector search for similar documents, returning metadata, similarity scores and source queries.
// @Tags         files
// @Produce      json
// @Param        q              query     string  true   "Search query text"
// @Param        limit          query     int     false  "Max search results limit (default 5)"
// @Param        nlp_expansion  query     bool    false  "Enable NLP semantic search query expansion"
// @Param        expansion_num  query     int     false  "Number of alternative queries to generate (default 3)"
// @Success      200    {array}   contract.SearchResponseItem
// @Failure      400    {object}  map[string]string "Missing query parameter 'q'"
// @Failure      401    {object}  map[string]string "Unauthorized"
// @Failure      500    {object}  map[string]string "Search failed"
// @Security     BasicAuth
// @Router       /files/search [get]
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

	nlpExpansion := false
	if nlpStr := c.Query("nlp_expansion"); nlpStr != "" {
		if parsed, err := strconv.ParseBool(nlpStr); err == nil {
			nlpExpansion = parsed
		}
	}

	expansionNum := 3
	if expNumStr := c.Query("expansion_num"); expNumStr != "" {
		if parsedExpNum, err := strconv.Atoi(expNumStr); err == nil && parsedExpNum > 0 {
			expansionNum = parsedExpNum
		}
	}

	items, err := h.fileUsecase.Search(c.Request.Context(), dbUser.ID, query, limit, nlpExpansion, expansionNum)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "search failed"})
		return
	}

	c.JSON(http.StatusOK, items)
}
