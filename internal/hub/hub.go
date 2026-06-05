// Package hub fans the latest captured frame out to all connected viewers.
//
// Frames arrive faster than slow viewers can consume, so delivery is
// best-effort: each subscriber has a 1-slot mailbox and a new frame replaces an
// undelivered one. Nobody blocks the capture loop.
package hub

import "sync"

// Hub broadcasts byte frames (JPEGs) to subscribers.
type Hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
	last []byte
}

// New creates an empty hub.
func New() *Hub {
	return &Hub{subs: make(map[chan []byte]struct{})}
}

// Publish delivers frame to every subscriber, dropping it for any that are
// behind rather than blocking.
func (h *Hub) Publish(frame []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last = frame
	for ch := range h.subs {
		select {
		case ch <- frame:
		default:
			// subscriber is behind; replace its pending frame
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- frame:
			default:
			}
		}
	}
}

// Subscribe returns a channel that receives frames, seeded with the most recent
// frame if one exists so a new viewer paints immediately.
func (h *Hub) Subscribe() chan []byte {
	ch := make(chan []byte, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	if h.last != nil {
		ch <- h.last
	}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes ch.
func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}
