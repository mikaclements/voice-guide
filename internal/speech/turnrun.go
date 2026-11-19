package speech

import (
	"context"
	"errors"
	"sync"
	"time"
	"uuid"
)

// turnRun is one turn being taken: its request, the turn it retries, if it
// does, what interrupting it closes and cancels, and when the next turn may
// start.
type turnRun struct {
	request       uuid.UUID
	retrying      *turnRun
	interrupted   chan struct{}
	stopSpeaking  context.CancelFunc
	interruptOnce sync.Once
	at            time.Time // when the user cut in; set before interrupted is closed
	free          chan struct{}
	freeOnce      sync.Once

	// mu orders the reply's first packet, its text reaching the page, the
	// user talking on and the turn being settled.
	// firstAt is when the first packet went, and sounding closes then.
	// talked says the user talked on before the reply reached them.
	// settled says the reply reached them, or stopped with none, before they
	// talked on: the turn is to be saved.
	// unremoved says the turn couldn't be taken back.
	mu                         sync.Mutex
	firstAt                    time.Time
	sounding                   chan struct{}
	talked, settled, unremoved bool
}

// stopForTalkOn stops the reply as the user talks on after their spoken turn was
// taken, having begun to speak at began, before they could hear any of it:
// unless its first packet had been heard by then, by the same estimate as the
// part of a reply heard, the turn has been settled or let the next start, or
// the user cut in otherwise. tell tells the page, before any more of the
// reply's text can reach it. It says whether it stopped the reply.
func (r *turnRun) stopForTalkOn(began time.Time, roundTrip time.Duration, tell func()) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	heardFirst := !r.firstAt.IsZero() && !heardBy(began.Add(-captureDelay), roundTrip).Before(r.firstAt)
	if _, cut := r.interruptedAt(); heardFirst || r.talked || r.settled || cut || r.released() {
		return false
	}
	r.talked = true
	tell()
	r.interrupt(began)
	return true
}

// talkedOn says whether the user talked on before the reply reached them.
func (r *turnRun) talkedOn() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.talked
}

// settle says the turn is to be saved, unless the user talked on first.
// After it says yes, talking on no longer stops the reply.
func (r *turnRun) settle() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settled = !r.talked
	return r.settled
}

// leftInFlight records that the turn couldn't be taken back, or took the
// place of one that couldn't.
func (r *turnRun) leftInFlight() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unremoved = true
}

// stillInFlight says whether the turn couldn't be taken back, or took the
// place of one that couldn't.
func (r *turnRun) stillInFlight() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.unremoved
}

// firstSound returns a channel closed once the reply's first packet goes out.
func (r *turnRun) firstSound() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sounding == nil {
		r.sounding = make(chan struct{})
	}
	return r.sounding
}

// sound says whether the reply's next packet, going out at at, may go: not
// once the user has talked on.
func (r *turnRun) sound(at time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.talked {
		return false
	}
	if r.firstAt.IsZero() {
		r.firstAt = at
		if r.sounding == nil {
			r.sounding = make(chan struct{})
		}
		close(r.sounding)
	}
	return true
}

// show tells the page a piece of the reply's text with tell, unless the user
// has cut in or talked on, and says whether it did.
func (r *turnRun) show(tell func()) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, cut := r.interruptedAt(); cut {
		return false
	}
	tell()
	return true
}

// gatedConn is a reply's connection: its packets go out only while the user
// hasn't talked on, and the first is noted.
type gatedConn struct {
	Connection
	run   *turnRun
	clock clock
}

var errTalkedOn = errors.New("the user talked on before the reply reached them")

func (c gatedConn) Play(opus []byte, silence time.Duration) error {
	if !c.run.sound(c.clock.Now()) {
		return errTalkedOn
	}
	return c.Connection.Play(opus, silence)
}

// interrupt stops the turn's reply and its speech at once, and the turn it
// retries, which may still be speaking; the first call counts.
func (r *turnRun) interrupt(at time.Time) {
	r.interruptOnce.Do(func() {
		r.at = at
		close(r.interrupted)
		r.stopSpeaking()
		if r.retrying != nil {
			r.retrying.interrupt(at)
		}
	})
}

// interruptedAt returns when the user cut in, if they have.
func (r *turnRun) interruptedAt() (time.Time, bool) {
	select {
	case <-r.interrupted:
		return r.at, true
	default:
		return time.Time{}, false
	}
}

// release lets the next turn start; the first call counts.
func (r *turnRun) release() { r.freeOnce.Do(func() { close(r.free) }) }

func (r *turnRun) released() bool {
	select {
	case <-r.free:
		return true
	default:
		return false
	}
}
