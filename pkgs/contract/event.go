package contract

// SystemEvent represents a system-wide event payload pushed to RabbitMQ.
type SystemEvent struct {
	EventID   string                 `json:"event_id"`
	EventType string                 `json:"event_type"`
	UserID    string                 `json:"user_id,omitempty"`
	Timestamp string                 `json:"timestamp"`
	Payload   map[string]interface{} `json:"payload"`
}

// EmbeddingJob is the message published to the embedding work queue.
// When IsCopy is true, the consumer copies vectors from SrcFileID to FileID
// instead of re-generating the embedding.
type EmbeddingJob struct {
	JobID       string `json:"job_id"`
	FileID      string `json:"file_id"`
	UserID      string `json:"user_id"`
	StorageKey  string `json:"storage_key"`            // e.g. "sha3-256-<hash>"
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	IsCopy      bool   `json:"is_copy"`                 // true = copy vectors from SrcFileID
	SrcFileID   string `json:"src_file_id,omitempty"`
}
