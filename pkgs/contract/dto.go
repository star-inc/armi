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

// FileResponse represents the JSON output of a file record.
type FileResponse struct {
	ID          string    `json:"id"`
	Filename    string    `json:"filename"`
	Hash        string    `json:"hash"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type"`
	OwnerID     string    `json:"owner_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SearchResponseItem represents a vector search matched item.
type SearchResponseItem struct {
	FileResponse
	Distance    float32 `json:"distance"`
	Score       float32 `json:"score"`
	SourceQuery string  `json:"source_query"`
}
