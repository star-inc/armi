package http

import (
	"sync"

	"github.com/star-inc/armi/pkgs/contract"
)

// EventsHub manages active SSE client channels and broadcasts events to all of them.
type EventsHub struct {
	mu      sync.RWMutex
	clients map[chan contract.SystemEvent]string
}

// NewEventsHub creates a new EventsHub instance.
func NewEventsHub() *EventsHub {
	return &EventsHub{
		clients: make(map[chan contract.SystemEvent]string),
	}
}

// Register adds a client channel to the active clients map.
func (h *EventsHub) Register(userID string, ch chan contract.SystemEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[ch] = userID
}

// Unregister removes a client channel and cleans it up.
func (h *EventsHub) Unregister(ch chan contract.SystemEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
}

// Broadcast sends the given system event to all currently connected clients in a non-blocking way.
func (h *EventsHub) Broadcast(event contract.SystemEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch, userID := range h.clients {
		if event.UserID != "" && userID != "" && userID != event.UserID {
			continue
		}
		select {
		case ch <- event:
		default:
			// Non-blocking so slow clients do not hold up others
		}
	}
}
