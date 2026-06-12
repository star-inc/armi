package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/star-inc/armi/pkgs/contract"
	"github.com/star-inc/armi/pkgs/user"
)

// EventsHandler streams system progress events to SSE clients.
type EventsHandler struct {
	hub *EventsHub
}

// NewEventsHandler creates a new events SSE handler.
func NewEventsHandler(hub *EventsHub) *EventsHandler {
	if hub == nil {
		hub = NewEventsHub()
	}
	return &EventsHandler{hub: hub}
}

// Stream connects the client to the SSE event bus.
// @Summary      Connect to event bus
// @Description  Streams processing progress and status updates via SSE.
// @Tags         events
// @Produce      text/event-stream
// @Success      200 {object} StreamResponse "SSE Connection established"
// @Security     BasicAuth
// @Security     BearerAuth
// @Router       /events [get]
func (h *EventsHandler) Stream(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, contract.ErrorResponse{Error: "unauthorized"})
		return
	}
	dbUser, ok := val.(*user.User)
	if !ok || dbUser == nil {
		c.JSON(http.StatusUnauthorized, contract.ErrorResponse{Error: "unauthorized"})
		return
	}

	ch := make(chan contract.SystemEvent, 64)
	h.hub.Register(dbUser.ID, ch)
	defer h.hub.Unregister(ch)

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, contract.ErrorResponse{Error: "streaming not supported"})
		return
	}

	_, _ = fmt.Fprint(c.Writer, ": connected\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(c.Writer, "id: %s\nevent: %s\ndata: %s\n\n", event.EventID, event.EventType, data); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(c.Writer, ": ping\n\n")
			flusher.Flush()
		}
	}
}
