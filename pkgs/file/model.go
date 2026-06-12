package file

import "time"

// FileRecord domain entity representing metadata mapping of uploaded files.
type FileRecord struct {
	ID              string
	Filename        string
	Description     string
	Hash            string
	Size            int64
	ContentType     string
	AuthorID        string
	GroupIDs        []string
	Tags            []string
	EmbeddingStatus string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type OutboxJob struct {
	ID        string
	FileID    string
	Payload   string
	CreatedAt time.Time
}

type CleanupJob struct {
	ID             string
	FileID         string
	Hash           string
	StorageKey     string
	DeletePhysical bool
	Status         string
	RetryCount     int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
