package api

import (
	"sync"

	"github.com/qustavo/monster/internal/mostro"
)

// EventType describes why an Event was published.
type EventType string

const (
	EventCreated EventType = "created"
	EventUpdated EventType = "updated"
)

// Event is broadcast to subscribers whenever an order is created or its
// status changes.
type Event struct {
	Type  EventType     `json:"type"`
	Order *mostro.Order `json:"order"`
}

// Hub fans out order Events to subscribers (SSE clients). It's safe for
// concurrent use.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// NewHub creates an empty Hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[chan Event]struct{})}
}

// Subscribe registers a new listener and returns its channel along with a
// cancel func that must be called to unregister it.
func (h *Hub) Subscribe() (ch chan Event, cancel func()) {
	ch = make(chan Event, 16)

	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	cancel = func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
	return ch, cancel
}

// Publish sends ev to every current subscriber. Slow subscribers whose
// buffer is full are skipped rather than blocking the publisher.
func (h *Hub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
