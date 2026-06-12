package http

// StreamResponse documents a non-JSON streaming or acknowledgment response.
// It is used for Swagger annotations where a concrete response schema is required.
type StreamResponse struct {
	Message string `json:"message"`
}
