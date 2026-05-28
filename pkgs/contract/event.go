package contract

// SystemEvent represents a system-wide event payload pushed to RabbitMQ.
type SystemEvent struct {
	EventID   string                 `json:"event_id"`
	EventType string                 `json:"event_type"`
	UserID    string                 `json:"user_id,omitempty"`
	Timestamp string                 `json:"timestamp"`
	Payload   map[string]interface{} `json:"payload"`
}
