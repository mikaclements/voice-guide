package http_test

import (
	"context"
	nethttp "net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
)

// ticket returns a ticket for the session whose audio path is audio, as its start hands back.
func (f fixture) ticket(t *testing.T, audio string) string {
	t.Helper()
	return f.ticketFrom(t, audio, now)
}

// ticketFrom returns a ticket for the session whose audio path is audio, issued at.
func (f fixture) ticketFrom(t *testing.T, audio string, at time.Time) string {
	t.Helper()
	ticket, err := f.key.MintTicket(access.Caller{Workspace: workspace, User: user, Session: sessionOf(audio)}, at, at)
	if err != nil {
		t.Fatalf("MintTicket: %v", err)
	}
	return ticket.Reveal()
}

// sessionOf returns the session whose audio path is audio.
func sessionOf(audio string) uuid.UUID {
	return uuid.MustParse(strings.TrimSuffix(strings.TrimPrefix(audio, "/v1/sessions/"), "/audio"))
}

// The browser opens its session's audio connection with the session's ticket,
// and opens it again with the same ticket after a drop or a reload.
func TestATicketConnectsItsSessionsAudioAgainAndAgain(t *testing.T) {
	f := newFixture(t, 10)
	ticket := f.ticket(t, f.audio[0])
	var conns []string
	for range 2 {
		w := f.sendWith(context.Background(), "POST", ticket, f.audio[0], "application/sdp", "offer")
		if w.Code != nethttp.StatusCreated {
			t.Fatalf("POST with the ticket = %d %s, want 201", w.Code, w.Body)
		}
		conns = append(conns, w.Header().Get("Location"))
	}
	if w := f.sendWith(context.Background(), "DELETE", ticket, conns[0], "", ""); w.Code != nethttp.StatusNotFound {
		t.Errorf("DELETE of the first connection = %d, want 404: the second replaced it", w.Code)
	}
	if w := f.sendWith(context.Background(), "DELETE", ticket, conns[1], "", ""); w.Code != nethttp.StatusOK {
		t.Errorf("DELETE with the ticket = %d %s, want 200", w.Code, w.Body)
	}
}

// This documents what a ticket meets once its session has ended, by its end
// or by the idle sweep: the refusal is speech's, as for any caller, so the
// ticket changes nothing here.
func TestAnEndedSessionsTicketGetsNoAudio(t *testing.T) {
	ends := map[string]func(f fixture) error{
		"ended": func(f fixture) error {
			_, _, err := f.sessions.End(context.Background(), workspace, user, sessionOf(f.audio[0]))
			return err
		},
		"ended by the idle sweep": func(f fixture) error {
			_, err := f.sessions.EndIdle(context.Background(), 0)
			return err
		},
	}
	for name, end := range ends {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 10)
			ticket := f.ticket(t, f.audio[0])
			if err := end(f); err != nil {
				t.Fatalf("end: %v", err)
			}
			w := f.sendWith(context.Background(), "POST", ticket, f.audio[0], "application/sdp", "offer")
			if w.Code != nethttp.StatusConflict || !strings.Contains(w.Body.String(), "/v1/problems/session-ended") {
				t.Errorf("POST = %d %s, want 409 of type /v1/problems/session-ended", w.Code, w.Body)
			}
		})
	}
}

// A ticket two seconds past its lifetime passes the router's check, which
// allows for clocks apart; speech, by its own clock, answers it as the router
// does past that, rather than opening a connection that would end at once.
func TestAnOfferWithATicketJustPastItsLifetimeIsAnsweredExpired(t *testing.T) {
	f := newFixture(t, 10)
	ticket := f.ticketFrom(t, f.audio[0], now.Add(-4*time.Hour-2*time.Second))
	w := f.sendWith(context.Background(), "POST", ticket, f.audio[0], "application/sdp", "offer")
	if w.Code != nethttp.StatusUnauthorized || !strings.Contains(w.Body.String(), "/v1/problems/ticket-expired") || f.transport.Answered() != 0 {
		t.Errorf("POST = %d %s with %d answered, want 401 of type /v1/problems/ticket-expired and none answered", w.Code, w.Body, f.transport.Answered())
	}
}
