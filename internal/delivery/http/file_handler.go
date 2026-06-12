package http

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/star-inc/armi/internal/usecase"
	"github.com/star-inc/armi/pkgs/contract"
	"github.com/star-inc/armi/pkgs/file"
	"github.com/star-inc/armi/pkgs/user"
	"github.com/rs/xid"
	"github.com/spf13/viper"
)

// FileHandler handles HTTP endpoints for document file management.
type FileHandler struct {
	fileUsecase *usecase.FileUsecase
	publisher   file.EventPublisher
}

func parseGroupIDs(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		for _, part := range strings.Split(item, ",") {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return out
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
// @Failure      400   {object}  contract.ErrorResponse "Missing file or invalid request"
// @Failure      401   {object}  contract.ErrorResponse "Unauthorized"
// @Failure      409   {object}  contract.FileConflictResponse "File conflict (duplicate filename or hash)"
// @Failure      500   {object}  contract.ErrorResponse "Internal server error"
// @Security     BasicAuth
// @Router       /files [post]
func (h *FileHandler) Upload(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, contract.ErrorResponse{Error: "unauthorized"})
		return
	}
	dbUser := val.(*user.User)

	multipartFile, fileHeader, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, contract.ErrorResponse{Error: "missing file"})
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
	if _, err := io.Copy(&content, multipartFile); err != nil {
		slog.Error("failed to read upload stream", "error", err)
		c.JSON(http.StatusInternalServerError, contract.ErrorResponse{Error: "failed to read upload file"})
		return
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
		strings.TrimSpace(c.PostForm("description")),
		parseGroupIDs(c.PostFormArray("group_ids")),
		fileHeader.Header.Get("Content-Type"),
		content.Bytes(),
		transferID,
		tags,
	)
	if err != nil {
		var conflictErr *file.ConflictError
		if errors.As(err, &conflictErr) {
			c.JSON(http.StatusConflict, contract.FileConflictResponse{
				Error:           conflictErr.Error(),
				ConflictingID:   conflictErr.ConflictingFileID,
				ConflictingHash: conflictErr.ConflictingFileHash,
			})
			return
		}
		if errors.Is(err, file.ErrFileConflict) {
			c.JSON(http.StatusConflict, contract.ErrorResponse{Error: err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, contract.ErrorResponse{Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// List lists files accessible to the user.
// @Summary      List user files
// @Description  Retrieves a paginated list of file metadata records accessible to the authenticated user.
// @Tags         files
// @Produce      json
// @Param        tag        query   string  false  "Filter by tag"
// @Param        page       query   int     false  "Page number (starts from 1)"
// @Param        page_size  query   int     false  "Number of items per page (1-100)"
// @Success      200   {object}  contract.FileListResponse
// @Failure      400   {object}  contract.ErrorResponse "Invalid pagination parameters"
// @Failure      401   {object}  contract.ErrorResponse "Unauthorized"
// @Failure      500   {object}  contract.ErrorResponse "Database error"
// @Security     BasicAuth
// @Router       /files [get]
func (h *FileHandler) List(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, contract.ErrorResponse{Error: "unauthorized"})
		return
	}
	dbUser := val.(*user.User)

	tag := c.Query("tag")

	page := 1
	if rawPage := c.Query("page"); rawPage != "" {
		p, err := strconv.Atoi(rawPage)
		if err != nil || p <= 0 {
			c.JSON(http.StatusBadRequest, contract.ErrorResponse{Error: "invalid page, must be a positive integer"})
			return
		}
		page = p
	}

	pageSize := 20
	if rawPageSize := c.Query("page_size"); rawPageSize != "" {
		ps, err := strconv.Atoi(rawPageSize)
		if err != nil || ps <= 0 {
			c.JSON(http.StatusBadRequest, contract.ErrorResponse{Error: "invalid page_size, must be a positive integer"})
			return
		}
		if ps > 100 {
			c.JSON(http.StatusBadRequest, contract.ErrorResponse{Error: "invalid page_size, must be <= 100"})
			return
		}
		pageSize = ps
	}

	files, err := h.fileUsecase.ListPaginated(c.Request.Context(), dbUser.ID, tag, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, contract.ErrorResponse{Error: "database error"})
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
// @Failure      401   {object}  contract.ErrorResponse "Unauthorized"
// @Failure      403   {object}  contract.ErrorResponse "Forbidden (access denied)"
// @Failure      404   {object}  contract.ErrorResponse "File not found"
// @Failure      500   {object}  contract.ErrorResponse "Download error"
// @Security     BasicAuth
// @Router       /files/{id} [get]
func (h *FileHandler) Download(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, contract.ErrorResponse{Error: "unauthorized"})
		return
	}
	dbUser := val.(*user.User)
	fileID := c.Param("id")

	data, filename, contentType, size, err := h.fileUsecase.Download(c.Request.Context(), dbUser.ID, fileID)
	if err != nil {
		if errors.Is(err, file.ErrFileNotFound) {
			c.JSON(http.StatusNotFound, contract.ErrorResponse{Error: err.Error()})
			return
		}
		if errors.Is(err, file.ErrAccessDenied) {
			c.JSON(http.StatusForbidden, contract.ErrorResponse{Error: err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, contract.ErrorResponse{Error: "download error"})
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
// @Success      200   {object}  contract.FileMetadataResponse "File metadata wrapper (database_record and physical_storage)"
// @Failure      401   {object}  contract.ErrorResponse "Unauthorized"
// @Failure      403   {object}  contract.ErrorResponse "Forbidden (access denied)"
// @Failure      404   {object}  contract.ErrorResponse "File not found"
// @Failure      500   {object}  contract.ErrorResponse "Metadata error"
// @Security     BasicAuth
// @Router       /files/{id}/metadata [get]
func (h *FileHandler) GetMetadata(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, contract.ErrorResponse{Error: "unauthorized"})
		return
	}
	dbUser := val.(*user.User)
	fileID := c.Param("id")

	record, opMetadata, err := h.fileUsecase.GetMetadata(c.Request.Context(), dbUser.ID, fileID)
	if err != nil {
		if errors.Is(err, file.ErrFileNotFound) {
			c.JSON(http.StatusNotFound, contract.ErrorResponse{Error: err.Error()})
			return
		}
		if errors.Is(err, file.ErrAccessDenied) {
			c.JSON(http.StatusForbidden, contract.ErrorResponse{Error: err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, contract.ErrorResponse{Error: "metadata error"})
		return
	}

	c.JSON(http.StatusOK, contract.FileMetadataResponse{
		DatabaseRecord:  *record,
		PhysicalStorage: *opMetadata,
	})
}

// PatchMetadata updates mutable metadata fields (filename, description, tags) of a file.
// @Summary      Update file metadata
// @Description  Updates file metadata fields such as filename and tags for the specified file.
// @Tags         files
// @Accept       json
// @Produce      json
// @Param        id       path      string                            true  "File ID"
// @Param        request  body      contract.UpdateFileMetadataRequest true  "Metadata updates"
// @Success      200      {object}  contract.FileResponse
// @Failure      400      {object}  contract.ErrorResponse "Invalid request or metadata update payload"
// @Failure      401      {object}  contract.ErrorResponse "Unauthorized"
// @Failure      403      {object}  contract.ErrorResponse "Forbidden (access denied)"
// @Failure      404      {object}  contract.ErrorResponse "File not found"
// @Failure      500      {object}  contract.ErrorResponse "Update metadata error"
// @Security     BasicAuth
// @Router       /files/{id}/metadata [patch]
func (h *FileHandler) PatchMetadata(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, contract.ErrorResponse{Error: "unauthorized"})
		return
	}
	dbUser := val.(*user.User)
	fileID := c.Param("id")

	var req contract.UpdateFileMetadataRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, contract.ErrorResponse{Error: err.Error()})
		return
	}
	if req.Filename == nil && req.Description == nil && req.GroupIDs == nil && req.Tags == nil {
		c.JSON(http.StatusBadRequest, contract.ErrorResponse{Error: "at least one field is required"})
		return
	}

	record, err := h.fileUsecase.UpdateMetadata(c.Request.Context(), dbUser.ID, fileID, req.Filename, req.Description, req.GroupIDs, req.Tags)
	if err != nil {
		if errors.Is(err, file.ErrFileNotFound) {
			c.JSON(http.StatusNotFound, contract.ErrorResponse{Error: err.Error()})
			return
		}
		if errors.Is(err, file.ErrAccessDenied) {
			c.JSON(http.StatusForbidden, contract.ErrorResponse{Error: err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, contract.ErrorResponse{Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, record)
}

// Delete deletes a file.
// @Summary      Delete a file
// @Description  Deletes the file record from database and vector database. If there are no other references, it deletes the physical file.
// @Tags         files
// @Produce      json
// @Param        id    path      string  true  "File ID"
// @Success      200   {object}  contract.DeleteResponse "Message and physical_deleted status"
// @Failure      401   {object}  contract.ErrorResponse "Unauthorized"
// @Failure      403   {object}  contract.ErrorResponse "Forbidden (access denied)"
// @Failure      404   {object}  contract.ErrorResponse "File not found"
// @Failure      500   {object}  contract.ErrorResponse "Delete error"
// @Security     BasicAuth
// @Router       /files/{id} [delete]
func (h *FileHandler) Delete(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, contract.ErrorResponse{Error: "unauthorized"})
		return
	}
	dbUser := val.(*user.User)
	fileID := c.Param("id")

	physicalDeleted, err := h.fileUsecase.Delete(c.Request.Context(), dbUser.ID, fileID)
	if err != nil {
		if errors.Is(err, file.ErrFileNotFound) {
			c.JSON(http.StatusNotFound, contract.ErrorResponse{Error: err.Error()})
			return
		}
		if errors.Is(err, file.ErrAccessDenied) {
			c.JSON(http.StatusForbidden, contract.ErrorResponse{Error: err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, contract.ErrorResponse{Error: "delete error"})
		return
	}

	c.JSON(http.StatusOK, contract.DeleteResponse{
		Message:         "file deleted successfully",
		PhysicalDeleted: physicalDeleted,
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
// @Success      200    {object}  contract.SearchListResponse
// @Failure      400    {object}  contract.ErrorResponse "Missing query parameter 'q'"
// @Failure      401    {object}  contract.ErrorResponse "Unauthorized"
// @Failure      500    {object}  contract.ErrorResponse "Search failed"
// @Security     BasicAuth
// @Router       /files/search [get]
func (h *FileHandler) Search(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, contract.ErrorResponse{Error: "unauthorized"})
		return
	}
	dbUser := val.(*user.User)

	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusBadRequest, contract.ErrorResponse{Error: "query parameter 'q' is required"})
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
		c.JSON(http.StatusInternalServerError, contract.ErrorResponse{Error: "search failed"})
		return
	}

	nlpEnabled := nlpExpansion && viper.GetBool("llm.query_expansion.enabled")
	c.JSON(http.StatusOK, contract.SearchListResponse{
		NLPExpansion: nlpEnabled,
		Items:        items,
	})
}
