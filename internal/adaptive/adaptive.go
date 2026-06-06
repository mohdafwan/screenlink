// Package adaptive tunes the stream's bandwidth to what viewers can actually
// receive (Phase 4). It is a single "level" dial: the controller watches how
// many frames viewers drop and steps a quality/fps ladder up when there's
// headroom or down when the link is congested.
//
// Two levers, both low-latency (no transcoding, no buffering):
//   - JPEG quality — bytes per frame (capture.py changes it live)
//   - fps cap       — frames per second the host forwards (Gate, host-side)
//
// Resolution stays the configured ceiling: changing it live forces a GStreamer
// renegotiation/flicker, which isn't worth it for the moderate extra saving.
package adaptive

import (
	"sync"
	"sync/atomic"
	"time"
)

// Level is one rung of the bandwidth ladder.
type Level struct {
	Quality int // JPEG quality 1-100
	FPS     int // frames per second the host forwards
}

// Controller steps through bandwidth levels based on observed drop rate.
type Controller struct {
	levels []Level
	cur    int
}

// NewController builds a 5-rung ladder from the worst tier (qMin, low fps) up to
// the configured ceilings (qCeil, fpsCeil), and starts optimistically at the
// top so a healthy link runs full quality until proven otherwise.
func NewController(qCeil, qMin, fpsCeil int) *Controller {
	if qMin < 1 {
		qMin = 1
	}
	if qMin > qCeil {
		qMin = qCeil
	}
	if fpsCeil < 1 {
		fpsCeil = 1
	}
	fracs := []float64{0, 0.25, 0.5, 0.75, 1.0}
	levels := make([]Level, len(fracs))
	for i, f := range fracs {
		q := int(float64(qMin) + f*float64(qCeil-qMin) + 0.5)
		fps := int(float64(fpsCeil)*(0.2+0.8*f) + 0.5)
		if fps < 1 {
			fps = 1
		}
		levels[i] = Level{Quality: q, FPS: fps}
	}
	return &Controller{levels: levels, cur: len(levels) - 1}
}

// Tune folds in one interval's delivered/dropped counts and returns the level to
// apply plus whether it changed. Back off fast on congestion, grow cautiously.
func (c *Controller) Tune(delivered, dropped uint64) (Level, bool) {
	total := delivered + dropped
	var ratio float64
	if total > 0 {
		ratio = float64(dropped) / float64(total)
	}
	old := c.cur
	switch {
	case ratio > 0.25 && c.cur > 0:
		c.cur-- // congested — drop a level
	case ratio < 0.05 && total > 0 && c.cur < len(c.levels)-1:
		c.cur++ // smooth with headroom — climb a level
	}
	return c.levels[c.cur], c.cur != old
}

// Current returns the level without changing it.
func (c *Controller) Current() Level { return c.levels[c.cur] }

// Gate rate-limits frame forwarding to a target fps. Safe for concurrent use;
// the capture goroutine calls Allow, the controller calls SetTarget.
type Gate struct {
	target atomic.Int64
	mu     sync.Mutex
	last   time.Time
}

// NewGate starts the gate at fps frames per second.
func NewGate(fps int) *Gate {
	g := &Gate{}
	g.target.Store(int64(fps))
	return g
}

// SetTarget changes the fps cap.
func (g *Gate) SetTarget(fps int) { g.target.Store(int64(fps)) }

// Allow reports whether enough time has passed to forward another frame.
func (g *Gate) Allow() bool {
	fps := g.target.Load()
	if fps <= 0 {
		return true
	}
	gap := time.Second / time.Duration(fps)
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if g.last.IsZero() || now.Sub(g.last) >= gap {
		g.last = now
		return true
	}
	return false
}
