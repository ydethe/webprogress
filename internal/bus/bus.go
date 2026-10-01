// Package bus is the in-process publish/subscribe hub that turns an ingested
// update into a live screen change. The ingest handler publishes a RoutedPayload;
// every open dashboard connection subscribes and renders the updates addressed to
// its viewer. The hub lives only within a running server instance: it does not
// cross process boundaries or survive a restart.
package bus

import (
	"sync"

	"github.com/ydethe/webprogress/internal/models"
)

// RoutedPayload is an update bound to its owning user at ingest time.
type RoutedPayload struct {
	UserSub string
	Payload models.ClientPayload
}

// subBuffer is how many pending updates a subscriber may lag by before the hub
// starts dropping its updates. Dropping (rather than blocking) keeps a slow
// dashboard from ever stalling ingest.
const subBuffer = 64

// Hub fans out published payloads to all current subscribers.
type Hub struct {
	mu   sync.RWMutex
	subs map[chan RoutedPayload]struct{}
}

// New returns an empty Hub.
func New() *Hub {
	return &Hub{subs: make(map[chan RoutedPayload]struct{})}
}

// Subscribe registers a new subscriber and returns its channel together with a
// cancel func that unsubscribes and closes the channel. The caller must call
// cancel when done (e.g. when a dashboard connection closes).
func (h *Hub) Subscribe() (<-chan RoutedPayload, func()) {
	ch := make(chan RoutedPayload, subBuffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

// Publish delivers a payload to every subscriber. Delivery is non-blocking: a
// subscriber whose buffer is full misses this update rather than blocking ingest.
func (h *Hub) Publish(rp RoutedPayload) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- rp:
		default:
		}
	}
}
