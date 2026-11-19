package speech

import (
	"context"
	"sync"
	"time"
	"uuid"
)

// Held counts the connections this server holds, so a test sees a place taken
// or given back.
func (s *Service) Held() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Served returns a channel closed once the session's connection held now has
// been let go and its turns saved, so a test waits for it instead of polling
// Held; nil if none is held.
func (s *Service) Served(session uuid.UUID) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.conns[session]; ok {
		return h.served
	}
	return nil
}

// ManualClock is a clock a test moves by hand: Until waits until the test has
// set it to the time waited for.
type ManualClock struct {
	mu    sync.Mutex
	now   time.Time
	moved chan struct{} // closed, and replaced, each time the clock is set
	// parked is told each time a wait finds its time yet to come, so a test
	// knows something waits before it moves the clock.
	parked chan struct{}
}

// NewManualClock returns a clock at now.
func NewManualClock(now time.Time) *ManualClock {
	return &ManualClock{now: now, moved: make(chan struct{}), parked: make(chan struct{}, 64)}
}

// Parked is told each time a wait finds its time yet to come.
func (c *ManualClock) Parked() <-chan struct{} { return c.parked }

// Now returns the time the clock was last set to.
func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set moves the clock to now.
func (c *ManualClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
	close(c.moved)
	c.moved = make(chan struct{})
}

// Pass moves the clock to now without waking what waits on it, as when a
// timer due has yet to fire: reading the clock sees the new time first.
func (c *ManualClock) Pass(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// Until returns once the clock is set to t or later, or false if ctx ends first.
func (c *ManualClock) Until(ctx context.Context, t time.Time) bool {
	for {
		c.mu.Lock()
		now, moved := c.now, c.moved
		if now.Before(t) {
			select {
			case c.parked <- struct{}{}:
			default:
			}
		}
		c.mu.Unlock()
		if !now.Before(t) {
			return true
		}
		select {
		case <-moved:
		case <-ctx.Done():
			return false
		}
	}
}

// UseClock has the service read the time from c, before any connection.
func (s *Service) UseClock(c *ManualClock) { s.clock = c }
