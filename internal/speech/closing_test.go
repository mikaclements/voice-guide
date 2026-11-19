package speech_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/conversation"
	modelfake "github.com/mikaclements/voice-guide/internal/conversation/model/fake"
	"github.com/mikaclements/voice-guide/internal/speech/transport/fake"
	ttsfake "github.com/mikaclements/voice-guide/internal/speech/tts/fake"
)

// heldSave holds the first reply's save until release is closed, telling
// saving once it waits, as a slow database does.
type heldSave struct {
	conversation.Turns
	saving, release chan struct{}
	once            *sync.Once
}

func (h heldSave) Finish(ctx context.Context, t conversation.Turn) error {
	first := false
	h.once.Do(func() { first = true })
	if first {
		close(h.saving)
		<-h.release
	}
	return h.Turns.Finish(ctx, t)
}

// waitFor fails the test unless ch is closed or sends within wait.
func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(wait):
		t.Fatalf("waited %s for %s", wait, what)
	}
}

// settle is how long a test watches for a turn taken while a connection
// closes: nothing tells it a turn was let go, so it watches a while, many
// times what taking one takes on a fake, before it lets the close finish.
const settle = 200 * time.Millisecond

// sentWithin returns the messages c is sent within d.
func sentWithin(t *testing.T, c *fake.Connection, d time.Duration) []message {
	t.Helper()
	var got []message
	deadline := time.After(d)
	for {
		select {
		case raw := <-c.Sent():
			var m message
			if err := json.Unmarshal([]byte(raw), &m); err != nil {
				t.Fatalf("speech sent %s, not JSON: %v", raw, err)
			}
			got = append(got, m)
		case <-deadline:
			return got
		}
	}
}

// Closing a real connection waits for the browser, up to a second or more;
// turns waiting for the one before to be saved when the cap passes aren't
// taken while it closes.
func TestTurnsWaitingWhenTheTicketExpiresAreNeverTakenWhileTheConnectionCloses(t *testing.T) {
	held := heldSave{saving: make(chan struct{}), release: make(chan struct{}), once: &sync.Once{}}
	model := &modelfake.Model{Pieces: replyPieces}
	h := newLoggingTurnHarness(t, model, ttsfake.TextToSpeech{}, slog.New(slog.DiscardHandler), func(turns conversation.Turns) conversation.Turns {
		held.Turns = turns
		return held
	})
	closing := make(chan struct{})
	h.transport.HoldClose = closing
	c, clock, ticket := connectWithTicket(t, h, opened.Add(time.Hour))

	first := uuid.NewV7()
	c.Say(turnMessage(first, "Where should we start?"))
	waitFor(t, held.saving, "the first reply's save")
	// Two turns take the places to wait in; a third is refused, which says the two have begun.
	for range 3 {
		c.Say(turnMessage(uuid.NewV7(), "Are you there?"))
	}
	until(t, c, "refused")

	clock.Set(ticket.Expires)
	h.transport.WaitClosing()
	close(held.release) // the first reply is saved, and the turns behind it are free to go
	for _, m := range sentWithin(t, c, settle) {
		if m.RequestID != first.String() {
			t.Errorf("sent %+v while the connection closed; want nothing about the turns waiting", m)
		}
	}
	close(closing)
	closeService(t, h.svc) // returns once every turn has ended
	if asked, _ := model.Requests(); len(asked) != 1 || len(h.turns.All()) != 1 {
		t.Errorf("the model was asked %d times and %d turns saved; want only the first turn", len(asked), len(h.turns.All()))
	}
}

// A turn read once its connection has ended is never begun. Go picks among
// ready cases at random, so the loop may read a turn sent as the page hung up
// only after the connection has ended; the test hangs up many times so that
// order surely comes up. Two turns already wait, so one begun would be refused
// as busy at once, on the loop's own goroutine: a refusal sent too late says
// it was begun.
func TestATurnReadOnceTheConnectionHasEndedIsNeverBegun(t *testing.T) {
	for range 30 {
		held := heldSave{saving: make(chan struct{}), release: make(chan struct{}), once: &sync.Once{}}
		h := newLoggingTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{}, slog.New(slog.DiscardHandler), func(turns conversation.Turns) conversation.Turns {
			held.Turns = turns
			return held
		})
		closing := make(chan struct{})
		h.transport.HoldClose = closing
		id, _, err := h.svc.Connect(context.Background(), h.caller, h.session, "offer")
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		c := h.transport.Conn(0)
		c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
		waitFor(t, held.saving, "the first reply's save")
		for range 3 { // two wait; the third is refused, which says both have begun
			c.Say(turnMessage(uuid.NewV7(), "Are you there?"))
		}
		until(t, c, "refused")

		hungUp := make(chan error, 1)
		go func() { hungUp <- h.svc.Hangup(context.Background(), h.caller, h.session, id) }()
		h.transport.WaitClosing()
		last := uuid.NewV7()
		c.Say(turnMessage(last, "Hello?"))
		close(closing)
		close(held.release)
		select {
		case err := <-hungUp:
			if err != nil {
				t.Fatalf("Hangup: %v", err)
			}
		case <-time.After(wait):
			t.Fatalf("Hangup didn't return within %s", wait)
		}
		for _, raw := range c.Late() {
			var m message
			if err := json.Unmarshal([]byte(raw), &m); err == nil && m.RequestID == last.String() {
				t.Fatalf("sent %s once the connection had ended; want the turn read then never begun", raw)
			}
		}
		closeService(t, h.svc)
	}
}

// A spoken turn whose end-of-turn check answers while the connection closes
// for its ticket isn't taken either.
func TestASpokenTurnFinishingAsTheTicketExpiresIsNeverTaken(t *testing.T) {
	model := &modelfake.Model{Pieces: replyPieces}
	check := judging{asked: make(chan chan bool, 4)}
	h := newHearingTurnHarness(t, model, ttsfake.TextToSpeech{}, slog.New(slog.DiscardHandler), nil, speechThenQuiet, saying, check)
	closing := make(chan struct{})
	h.transport.HoldClose = closing
	c, clock, ticket := connectWithTicket(t, h, opened.Add(time.Hour))

	speakFrames(c, 12)
	var answer chan bool
	select {
	case answer = <-check.asked:
	case <-time.After(wait):
		t.Fatal("the end-of-turn check wasn't asked")
	}
	clock.Set(ticket.Expires)
	h.transport.WaitClosing()
	answer <- true // the user has finished, as the connection closes
	for _, m := range sentWithin(t, c, settle) {
		if m.Type != "hearing" {
			t.Errorf("sent %+v while the connection closed; want no turn taken", m)
		}
	}
	close(closing)
	closeService(t, h.svc)
	if asked, _ := model.Requests(); len(asked) != 0 || len(h.turns.All()) != 0 {
		t.Errorf("the model was asked %d times and %d turns saved; want none", len(asked), len(h.turns.All()))
	}
}
