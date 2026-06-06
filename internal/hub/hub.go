// Package hub fans the latest captured frame out to all connected viewers.
//
// Frames arrive faster than slow viewers can consume, so delivery is
// best-effort: each subscriber has a 1-slot mailbox and a new frame replaces an
// undelivered one. Nobody blocks the capture loop.
//
// Each subscriber also counts delivered vs dropped frames; the adaptive
// bandwidth controller (Phase 4) reads these to decide whether viewers are
// keeping up and tune encoder quality / fps accordingly.
package hub

import (
	"sync"
	"sync/atomic"
)

// Sub is one viewer's subscription: a 1-slot frame mailbox plus delivery stats.
type Sub struct {
	C         chan []byte
	delivered atomic.Uint64
	dropped   atomic.Uint64
}

// Hub broadcasts byte frames (JPEGs) to subscribers.
type Hub struct {
	mu   sync.Mutex
	subs map[*Sub]struct{}
	last []byte
}

// Stats is an aggregate delivery snapshot across all subscribers.
type Stats struct {
	Subscribers int
	Delivered   uint64
	Dropped     uint64
}

// New creates an empty hub.
func New() *Hub {
	return &Hub{subs: make(map[*Sub]struct{})}
}

// Publish delivers frame to every subscriber, dropping it for any that are
// behind rather than blocking.
func (h *Hub) Publish(frame []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last = frame
	for s := range h.subs {
		select {
		case s.C <- frame:
			s.delivered.Add(1)
		default:
			// subscriber is behind; replace its pending frame and count a drop
			select {
			case <-s.C:
			default:
			}
			select {
			case s.C <- frame:
			default:
			}
			s.dropped.Add(1)
		}
	}
}

// Subscribe returns a subscription seeded with the most recent frame (if any)
// so a new viewer paints immediately.
func (h *Hub) Subscribe() *Sub {
	s := &Sub{C: make(chan []byte, 1)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	if h.last != nil {
		s.C <- h.last
	}
	h.mu.Unlock()
	return s
}

// Unsubscribe removes s.
func (h *Hub) Unsubscribe(s *Sub) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
}

// Stats returns the cumulative delivered/dropped totals across current
// subscribers. The controller diffs successive calls to get per-interval rates.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := Stats{Subscribers: len(h.subs)}
	for s := range h.subs {
		st.Delivered += s.delivered.Load()
		st.Dropped += s.dropped.Load()
	}
	return st
}
