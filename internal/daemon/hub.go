package daemon

import "sync"

// Event is an SSE event broadcast to all subscribers.
type Event struct {
	Type string
	Data any
}

type hub struct {
	mu      sync.RWMutex
	clients map[chan Event]struct{}
}

func newHub() *hub {
	return &hub{clients: make(map[chan Event]struct{})}
}

func (h *hub) subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.clients, ch)
		close(ch)
	}
}

func (h *hub) broadcast(e Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- e:
		default:
		}
	}
}
