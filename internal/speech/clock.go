package speech

import (
	"context"
	"time"
)

// clock is the time as speech reads it, and its waits: the system's, or a
// test's, so pacing is tested without waiting on the real clock.
type clock interface {
	Now() time.Time
	// Until returns once t has come, or false if ctx ends first.
	Until(ctx context.Context, t time.Time) bool
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) Until(ctx context.Context, t time.Time) bool {
	timer := time.NewTimer(time.Until(t))
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
