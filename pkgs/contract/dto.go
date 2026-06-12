package contract

import "time"

// RegisterRequest represents the user registration input.
type RegisterRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// RegisterResponse represents the user registration output.
type RegisterResponse struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
}

// UpdateMeRequest represents partial updates for the current user profile.
type UpdateMeRequest struct {
	Username *string `json:"username,omitempty"`
	Password *string `json:"password,omitempty"`
}

// MeResponse represents current user profile output.
type MeResponse struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FileResponse represents the JSON output of a file record.
type FileResponse struct {
	ID              string    `json:"id"`
	Filename        string    `json:"filename"`
	Description     string    `json:"description"`
	Hash            string    `json:"hash"`
	Size            int64     `json:"size"`
	ContentType     string    `json:"content_type"`
	AuthorID        string    `json:"author_id"`
	GroupIDs        []string  `json:"group_ids,omitempty"`
	Tags            []string  `json:"tags"`
	EmbeddingStatus string    `json:"embedding_status"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// UpdateFileMetadataRequest represents partial file metadata updates.
type UpdateFileMetadataRequest struct {
	Filename    *string  `json:"filename,omitempty"`
	Description *string  `json:"description,omitempty"`
	GroupIDs    []string `json:"group_ids,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// FileListResponse represents paginated file list output.
type FileListResponse struct {
	Items      []FileResponse `json:"items"`
	Total      int64          `json:"total"`
	Page       int            `json:"page"`
	PageSize   int            `json:"page_size"`
	TotalPages int            `json:"total_pages"`
}

// SearchResponseItem represents a vector search matched item.
type SearchResponseItem struct {
	FileResponse
	Distance    float32 `json:"distance"`
	Score       float32 `json:"score"`
	SourceQuery string  `json:"source_query"`
	ChunkID     string  `json:"chunk_id,omitempty"`
	ChunkText   string  `json:"chunk_text,omitempty"`
}

// SearchListResponse represents vector search results with NLP expansion metadata.
type SearchListResponse struct {
	NLPExpansion bool                 `json:"nlp_expansion"`
	Items        []SearchResponseItem `json:"items"`
}

// StorageMetadataResponse represents storage backend metadata for a file.
type StorageMetadataResponse struct {
	ContentLength int64  `json:"content_length"`
	LastModified  string `json:"last_modified"`
	Error         string `json:"error,omitempty"`
}

// FileMetadataResponse represents the metadata wrapper returned by the metadata endpoint.
type FileMetadataResponse struct {
	DatabaseRecord  FileResponse            `json:"database_record"`
	PhysicalStorage StorageMetadataResponse `json:"physical_storage"`
}

// DeleteResponse represents the delete endpoint response.
type DeleteResponse struct {
	Message         string `json:"message"`
	PhysicalDeleted bool   `json:"physical_deleted"`
}

// ErrorResponse represents a standard API error payload.
type ErrorResponse struct {
	Error string `json:"error"`
}

// HealthResponse represents the health check payload.
type HealthResponse struct {
	Status string `json:"status"`
}

// FileConflictResponse represents upload conflict details.
type FileConflictResponse struct {
	Error           string `json:"error"`
	ConflictingID   string `json:"conflicting_id,omitempty"`
	ConflictingHash string `json:"conflicting_hash,omitempty"`
}
