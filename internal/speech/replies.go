package speech

import (
	"context"
	"sync"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
)

// waitingTurns bounds the turns waiting behind the one being taken on a
// connection; more are refused as busy. A person can't type faster than an
// interrupted turn is saved, about 15 ms measured, so more than a couple
// waiting means a script or a stuck key: a round guess.
const waitingTurns = 2

// replies takes one connection's turns, typed and spoken, in order, and keeps
// what the agent's replies have sounded like to the user. A turn begins under
// its lock on the goroutine that read it, the data channel's or the
// microphone's, so a spoken turn has begun by the time it is handed over.
type replies struct {
	s       *Service
	conn    Connection
	caller  access.Caller
	session uuid.UUID
	turns   *sync.WaitGroup
	waiting chan struct{} // a place for each turn waiting
	active  chan struct{} // the user began speaking, for marking the session active
	aired   aired

	mu   sync.Mutex
	last *turnRun // the turn the next one interrupts, or waits for
}

// begin takes the turn m once the one before it is saved, interrupting that
// one, or refuses it when too many wait.
func (r *replies) begin(ctx context.Context, m turnIn, received time.Time, said *heardTurn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Once the ticket has expired no turn is taken, whichever way it came: the connection is
	// closing for it.
	if r.caller.ExpiredAt(r.s.clock.Now()) {
		return
	}
	before := r.last
	wait := before != nil && !before.released()
	// The same request again while its turn is answered is the page
	// retrying, not the user cutting in on their own turn: it waits,
	// and conversation says whether it was taken. One retry waits at
	// a time, so retries can't take the places the user's turns need.
	retry := wait && before.request == m.RequestID
	// An opening is only ever the session's first turn, so one sent while another is taken is
	// refused at once, never cutting in on it as the user's own turn would.
	if m.Opening && wait && !retry {
		r.s.tell(r.conn, out{Type: "refused", RequestID: m.RequestID.String(), Reason: reasonOpeningRefused, Detail: openingRefused})
		return
	}
	full := retry && before.retrying != nil
	if wait && !full {
		select {
		case r.waiting <- struct{}{}:
		default:
			full = true
		}
	}
	if full {
		r.s.tell(r.conn, out{Type: "refused", RequestID: m.RequestID.String(), Reason: reasonBusy, Detail: "too many turns are waiting; send it again once the agent has answered"})
		return
	}
	speaking, stopSpeaking := context.WithCancel(ctx)
	run := &turnRun{request: m.RequestID, interrupted: make(chan struct{}), stopSpeaking: stopSpeaking, free: make(chan struct{})}
	if retry {
		run.retrying = before
	} else if before != nil {
		before.interrupt(received)
	}
	r.last = run
	r.turns.Go(func() {
		defer stopSpeaking()
		defer run.release()
		if wait {
			<-before.free // saved, cut where it was, before this turn's prompt
			<-r.waiting
		}
		if ctx.Err() != nil || r.caller.ExpiredAt(r.s.clock.Now()) {
			return // the connection ended, or is ending; the page sends it again on its next
		}
		r.runTurn(ctx, speaking, m, received, run, said)
	})
}

// interruptLast interrupts the turn begun last, if it is still being taken, at at.
func (r *replies) interruptLast(at time.Time) {
	r.mu.Lock()
	last := r.last
	r.mu.Unlock()
	if last != nil && !last.released() {
		last.interrupt(at)
	}
}

// begun returns the turn begun last if it is request's.
func (r *replies) begun(request uuid.UUID) *turnRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last == nil || r.last.request != request {
		return nil
	}
	return r.last
}

// beginSpoken begins a turn the user spoke before it returns, so whatever the
// microphone brings next comes after it.
func (r *replies) beginSpoken(ctx context.Context, h heardTurn) {
	r.begin(ctx, turnIn{Type: "turn", RequestID: h.request, Words: h.words}, r.s.clock.Now(), &h)
}
