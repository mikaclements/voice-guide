package speech_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/conversation"
	modelfake "github.com/mikaclements/voice-guide/internal/conversation/model/fake"
	"github.com/mikaclements/voice-guide/internal/speech"
	"github.com/mikaclements/voice-guide/internal/speech/transport/fake"
	ttsfake "github.com/mikaclements/voice-guide/internal/speech/tts/fake"
)

// opened is when the connections in these tests open, on a clock the test sets.
var opened = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

// connectWithTicket opens the harness's session's audio with a ticket that
// expires at expires, on a clock the test sets, starting at opened.
func connectWithTicket(t *testing.T, h turnHarness, expires time.Time) (*fake.Connection, *speech.ManualClock, access.Caller) {
	t.Helper()
	clock := speech.NewManualClock(opened)
	h.svc.UseClock(clock)
	ticket := h.caller
	ticket.Session, ticket.Expires = h.session, expires
	if _, _, err := h.svc.Connect(context.Background(), ticket, h.session, "offer"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return h.transport.Conn(0), clock, ticket
}

// closedWith waits for c to close and checks it closed once, with why.
func closedWith(t *testing.T, h turnHarness, c *fake.Connection, why speech.CloseReason) {
	t.Helper()
	select {
	case <-c.Done():
	case <-time.After(wait):
		t.Fatalf("the connection is still open %s after its ticket expired", wait)
	}
	if got := h.transport.Reasons(); !slices.Equal(got, []speech.CloseReason{why}) {
		t.Errorf("closed with %v, want %s", got, why)
	}
}

// A ticket's lifetime caps the audio connection it opened as well as its
// requests, so a stolen ticket's connection stops then too.
func TestAnAudioConnectionEndsWhenTheTicketThatOpenedItExpires(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{})
	c, clock, ticket := connectWithTicket(t, h, opened.Add(4*time.Hour))

	clock.Set(ticket.Expires.Add(-time.Second))
	c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
	until(t, c, "done")
	clock.Set(ticket.Expires)
	closedWith(t, h, c, speech.Expired)
}

// The audio stops at the cap, so a reply playing then is saved cut where the
// user stopped hearing it, as when they hang up.
func TestAReplyPlayingWhenTheTicketExpiresIsSavedCutWhereHeard(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: slowWords})
	// "Tell me" is 50 packets, a second; the cap falls 5 packets into "about".
	c, clock, ticket := connectWithTicket(t, h, opened.Add(55*speech.PacketDuration))
	c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
	until(t, c, "done")
	// The first packet goes out at opened; then a packet at a time, since a jump in the clock
	// is a gap in the speech, not a burst of it.
	playedAtLeast(t, c, 1)
	for i := 1; i < 2*slowWords+5; i++ {
		clock.Set(opened.Add(time.Duration(i) * speech.PacketDuration))
		playedAtLeast(t, c, i+1)
	}

	clock.Set(ticket.Expires)
	closedWith(t, h, c, speech.Expired)
	closeService(t, h.svc) // returns once the turn is saved
	saved := h.turns.All()
	if len(saved) != 1 || saved[0].Status != conversation.TurnCut || saved[0].HeardReply() != "Tell me" {
		t.Errorf("saved %+v, want the reply cut after %q, where the user stopped hearing it", saved, "Tell me")
	}
}

// A turn read as the cap passes, before the connection has been closed for
// it, is never taken: the ticket that would take it has expired.
func TestATurnWaitingAsTheTicketExpiresIsNeverTaken(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{})
	c, clock, ticket := connectWithTicket(t, h, opened.Add(time.Hour))
	select {
	case <-clock.Parked(): // the wait for the cap
	case <-time.After(wait):
		t.Fatalf("nothing waited for the cap within %s", wait)
	}
	clock.Pass(ticket.Expires) // the cap has passed, and the wait for it has yet to wake
	c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
	c.Say(turnMessage(uuid.NewV7(), "Are you there?"))

	closedWith(t, h, c, speech.Expired)
	closeService(t, h.svc)
	if asked, _ := h.model.(*modelfake.Model).Requests(); len(asked) != 0 || len(h.turns.All()) != 0 {
		t.Errorf("the model was asked %d times and %d turns saved; want none", len(asked), len(h.turns.All()))
	}
}

// The credential check allows for clocks apart, so a ticket a few seconds past
// its lifetime can reach speech; by speech's own clock it gets no connection,
// which would end at once.
func TestAnOfferWithATicketExpiredBySpeechsClockIsRefused(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{})
	h.svc.UseClock(speech.NewManualClock(opened))
	ticket := h.caller
	ticket.Session, ticket.Expires = h.session, opened.Add(-2*time.Second)
	_, _, err := h.svc.Connect(context.Background(), ticket, h.session, "offer")
	if !errors.Is(err, speech.ErrTicketExpired) || h.transport.Answered() != 0 {
		t.Errorf("Connect = %v with %d offers answered; want ErrTicketExpired, none answered", err, h.transport.Answered())
	}
}
