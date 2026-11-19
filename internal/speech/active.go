package speech

import (
	"context"
	"errors"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/conversation"
)

// mostOftenEvery is the longest a connection waits between marking its
// session active while the user speaks: 30 s, a round guess, a write per
// talking user every 30 s, which costs the database nothing worth measuring.
const mostOftenEvery = 30 * time.Second

// markEvery is how often, at most, a connection marks its session active
// while the user speaks: a tenth of the idle time, so a session whose user is
// still talking always has nine tenths of it left, and never less often than
// every 30 s.
func markEvery(idle time.Duration) time.Duration { return min(idle/10, mostOftenEvery) }

// marker says when the session is next to be marked active: at the first
// speech, then at the first speech each interval after the last mark.
type marker struct {
	every time.Duration
	last  time.Time
}

func (m *marker) due(now time.Time) bool {
	if !m.last.IsZero() && now.Sub(m.last) < m.every {
		return false
	}
	m.last = now
	return true
}

// activeWrite bounds marking the session active, one row update, so a stalled
// database holds this connection's marker no longer than this. A round guess.
const activeWrite = 5 * time.Second

// keepActive marks the session active each time heard says the user began
// speaking, at most once a markEvery, until ctx ends: a user answering at
// length after a long think isn't idle. It runs off the microphone's
// goroutine, which only signals heard, so a slow write never holds the audio.
func (s *Service) keepActive(ctx context.Context, c access.Caller, session uuid.UUID, heard <-chan struct{}) {
	m := marker{every: markEvery(s.conversation.SessionIdle())}
	for {
		select {
		case <-ctx.Done():
			return
		case <-heard:
		}
		if !m.due(s.clock.Now()) {
			continue
		}
		write, cancel := context.WithTimeout(ctx, activeWrite)
		err := s.conversation.Active(write, c, session)
		cancel()
		// An ended session's next turn is refused, which ends the connection.
		if err != nil && !errors.Is(err, conversation.ErrSessionEnded) && ctx.Err() == nil {
			s.log.Warn("mark the session active", "session", session, "err", err)
		}
	}
}

// heardSpeech tells the connection's marker the user began speaking; with a
// signal already waiting, there is nothing to add.
func (r *replies) heardSpeech() {
	select {
	case r.active <- struct{}{}:
	default:
	}
}
