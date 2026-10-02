package notifications

import "sync"

// Hub fans WebSocket events out to every connection of one user (R13.5).
// Cross-process fan-out would use Valkey; that client is not in go.mod, which
// Track A owns, so this hub is in-process. One API process meets E9.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[*wsConn]struct{}
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{subs: map[string]map[*wsConn]struct{}{}}
}

func (h *Hub) add(userID string, c *wsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[userID] == nil {
		h.subs[userID] = map[*wsConn]struct{}{}
	}
	h.subs[userID][c] = struct{}{}
}

func (h *Hub) remove(userID string, c *wsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs[userID], c)
	if len(h.subs[userID]) == 0 {
		delete(h.subs, userID)
	}
}

// Publish writes payload to every connection currently subscribed for userID.
func (h *Hub) Publish(userID string, payload []byte) {
	h.mu.Lock()
	conns := make([]*wsConn, 0, len(h.subs[userID]))
	for c := range h.subs[userID] {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		_ = c.writeText(payload)
	}
}

// ConnectionCount is how many sockets a user has open. Tests use it.
func (h *Hub) ConnectionCount(userID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[userID])
}
