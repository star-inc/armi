package file

import "time"

// FileRecord domain entity representing metadata mapping of uploaded files.
type FileRecord struct {
	ID          string
	Filename    string
	Hash        string
	Size        int64
	ContentType string
	OwnerID     string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
