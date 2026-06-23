package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Event is a typed SSE payload consumed by the Console v2 frontend.
type Event struct {
	Type string          `json:"type"` // e.g. "reload", "config_change", "cert_change", "health_change"
	Time time.Time       `json:"time"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Hub broadcasts events to subscribed SSE clients.
type Hub struct {
	mu     sync.RWMutex
	subs   map[chan Event]struct{}
	closed bool
}

func newHub() *Hub {
	return &Hub{subs: make(map[chan Event]struct{})}
}

// Subscribe registers a new SSE receiver. The caller must range over ch and
// close it when done; closing unsubscribes automatically.
func (h *Hub) Subscribe() chan Event {
	ch := make(chan Event, 8)
	h.mu.Lock()
	if !h.closed {
		h.subs[ch] = struct{}{}
	} else {
		close(ch)
	}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan Event) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
	close(ch)
}

// Broadcast sends an event to all current subscribers, dropping for slow readers.
func (h *Hub) Broadcast(ev Event) {
	h.mu.RLock()
	if h.closed {
		h.mu.RUnlock()
		return
	}
	subs := make([]chan Event, 0, len(h.subs))
	for ch := range h.subs {
		subs = append(subs, ch)
	}
	h.mu.RUnlock()

	for _, ch := range subs {
		select {
		case ch <- ev:
		default: // slow reader: drop
		}
	}
}

// Close shuts down the hub and drains remaining subscribers.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	subs := make([]chan Event, 0, len(h.subs))
	for ch := range h.subs {
		subs = append(subs, ch)
	}
	clear(h.subs)
	h.mu.Unlock()

	for _, ch := range subs {
		close(ch)
	}
}

// handleEvents is the GET /api/events SSE endpoint.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Optional CORS header if the console is served cross-origin in some setups.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := s.hub.Subscribe()
	defer s.hub.unsubscribe(ch)

	// Send a synthetic ping so the frontend knows the connection is live.
	_ = sendSSE(w, flusher, Event{Type: "connected", Time: time.Now().UTC()})

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if err := sendSSE(w, flusher, ev); err != nil {
				return
			}
		case t := <-ticker.C:
			if err := sendSSE(w, flusher, Event{Type: "ping", Time: t.UTC()}); err != nil {
				return
			}
		case <-r.Context().Done():
			return
		case <-s.quit:
			return
		}
	}
}

// sendSSE writes one SSE event frame over w.
func sendSSE(w http.ResponseWriter, flusher http.Flusher, ev Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	if err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// broadcast emits a typed event to all SSE listeners, best-effort.
func (s *Server) broadcast(typ string, data any) {
	if s.hub == nil {
		return
	}
	var raw json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return
		}
		raw = b
	}
	s.hub.Broadcast(Event{
		Type: typ,
		Time: time.Now().UTC(),
		Data: raw,
	})
}
