package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/api"
)

// ticketCaller is the caller a ticket for the routes' session is minted for.
var ticketCaller = access.Caller{Workspace: caller.Workspace, User: caller.User, Session: uuid.MustParse(session)}

// holder is the caller a ticket the fixture mints for c at now names: c, and
// when the ticket expires.
func holder(c access.Caller) access.Caller {
	c.Expires = now.Add(4 * time.Hour)
	return c
}

// ticket returns a ticket the fixture's router mints for c at the router's
// clock, as a start does.
func (f *fixture) ticket(t *testing.T, c access.Caller) string {
	t.Helper()
	ticket, ok, err := f.rt.Ticket(c, f.at)
	if err != nil || !ok {
		t.Fatalf("Ticket: %v, minted %v", err, ok)
	}
	return ticket.Reveal()
}

// assertExpired checks rec is the 401 for a ticket past its lifetime, which a
// page tells apart from a wrong credential to say the session has expired.
func assertExpired(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var p struct {
		Type, Title, Detail string
		Status              int
	}
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil || rec.Code != http.StatusUnauthorized ||
		p.Type != "/v1/problems/ticket-expired" || p.Status != http.StatusUnauthorized || p.Title == "" {
		t.Errorf("%d %+v (%v), want a 401 problem of type /v1/problems/ticket-expired", rec.Code, p, err)
	}
	if !strings.Contains(p.Detail, "expired") || !strings.Contains(p.Detail, "start a new session") {
		t.Errorf("detail %q, want it to say the session expired and to start a new one", p.Detail)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer error="invalid_token"` {
		t.Errorf("WWW-Authenticate = %q", got)
	}
}

func TestATicketReachesItsOwnSessionsRoutesAndNoOther(t *testing.T) {
	f := newFixture(t)
	own := f.ticket(t, ticketCaller)
	other := ticketCaller
	other.Session = uuid.NewV7()
	elsewhere := f.ticket(t, other)
	for _, r := range routes {
		if !r.tickets {
			continue
		}
		method, _, _ := strings.Cut(r.pattern, " ")
		// As a session that doesn't exist is answered, so a ticket learns nothing of other sessions.
		assertProblem(t, f.send(method, r.path, "Bearer "+elsewhere), http.StatusNotFound)
		if _, ok := f.reached[r.pattern]; ok {
			t.Errorf("%s: another session's ticket reached the handler", r.pattern)
		}
		if f.send(method, r.path, "Bearer "+own); f.reached[r.pattern] != holder(ticketCaller) {
			t.Errorf("%s: its session's ticket reached the handler with %+v, want %+v", r.pattern, f.reached[r.pattern], holder(ticketCaller))
		}
	}
	if f.store.Reads() != 0 {
		t.Errorf("%d product key reads; a ticket is checked without reading the database", f.store.Reads())
	}
}

// A session ID that isn't a UUID names no session, so it is no ticket's: the
// router answers it, so no handler reads the path's session.
func TestATicketOnAPathWhoseSessionIDIsNoUUIDIsAnsweredAsNoSuchSession(t *testing.T) {
	f := newFixture(t)
	ticket := f.ticket(t, ticketCaller)
	for _, r := range routes {
		if !r.tickets {
			continue
		}
		method, _, _ := strings.Cut(r.pattern, " ")
		assertProblem(t, f.send(method, strings.Replace(r.path, session, "lamp", 1), "Bearer "+ticket), http.StatusNotFound)
	}
	if len(f.reached) != 0 {
		t.Errorf("handlers ran: %v", f.reached)
	}
}

func TestATicketOnAProductsRouteIsRefused(t *testing.T) {
	f := newFixture(t)
	ticket := f.ticket(t, ticketCaller)
	for _, r := range routes {
		if r.tickets {
			continue
		}
		method, _, _ := strings.Cut(r.pattern, " ")
		rec := f.send(method, r.path, "Bearer "+ticket)
		assertProblem(t, rec, http.StatusUnauthorized)
		if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer error="invalid_token"` {
			t.Errorf("%s: WWW-Authenticate = %q", r.pattern, got)
		}
	}
	if len(f.reached) != 0 {
		t.Errorf("handlers ran: %v", f.reached)
	}
}

// The routes that take turns, open audio or hang it up refuse a ticket past its
// lifetime; conversation's and speech's tests rely on this check rather than
// repeating it.
func TestATicketWorksToItsCapThenIsRefusedAsExpired(t *testing.T) {
	f := newFixture(t)
	ticket := f.ticket(t, ticketCaller)
	for _, r := range routes {
		if !r.tickets || r.ending {
			continue
		}
		method, _, _ := strings.Cut(r.pattern, " ")
		f.at = now.Add(4 * time.Hour)
		if f.send(method, r.path, "Bearer "+ticket); f.reached[r.pattern] != holder(ticketCaller) {
			t.Errorf("%s at the cap: reached %+v, want the ticket's caller", r.pattern, f.reached[r.pattern])
		}
		delete(f.reached, r.pattern)
		f.at = now.Add(4*time.Hour + 6*time.Second) // past the cap and the leeway for clocks apart
		assertExpired(t, f.send(method, r.path, "Bearer "+ticket))
		if _, ok := f.reached[r.pattern]; ok {
			t.Errorf("%s: an expired ticket reached the handler", r.pattern)
		}
	}
	if strings.Contains(f.logs.String(), ticket) {
		t.Errorf("logs %q hold the ticket", f.logs.String())
	}
}

// The session's end takes its ticket for a quarter of an hour past its
// lifetime, so the session ends at once rather than when the idle sweep finds
// it; the ticket still works only in its own session there.
func TestTheSessionsEndTakesAGenuineTicketShortlyPastItsLifetime(t *testing.T) {
	f := newFixture(t)
	own := f.ticket(t, ticketCaller)
	other := ticketCaller
	other.Session = uuid.NewV7()
	elsewhere := f.ticket(t, other)
	for _, r := range routes {
		if !r.ending {
			continue
		}
		method, _, _ := strings.Cut(r.pattern, " ")
		f.at = now.Add(4*time.Hour + 15*time.Minute)
		assertProblem(t, f.send(method, r.path, "Bearer "+elsewhere), http.StatusNotFound)
		if f.send(method, r.path, "Bearer "+own); f.reached[r.pattern] != holder(ticketCaller) {
			t.Errorf("%s 15 minutes past the lifetime: reached %+v, want the ticket's caller", r.pattern, f.reached[r.pattern])
		}
		delete(f.reached, r.pattern)
		f.at = f.at.Add(time.Second)
		assertExpired(t, f.send(method, r.path, "Bearer "+own))
		if _, ok := f.reached[r.pattern]; ok {
			t.Errorf("%s: a ticket past the end's grace reached the handler", r.pattern)
		}
	}
}

// Rotating voice-guide's signing key stops every ticket: one signed with the
// old key is refused as never signed on every route, never reported expired,
// not even on a route that takes a ticket past its lifetime.
func TestRotatingTheSigningKeyRefusesEveryTicket(t *testing.T) {
	ticket := newFixture(t).ticket(t, ticketCaller)
	f := newFixtureWithKey(t, "fedcba9876543210fedcba9876543210")
	for _, at := range []time.Time{now, now.Add(5 * time.Hour)} {
		f.at = at
		for _, r := range routes {
			method, _, _ := strings.Cut(r.pattern, " ")
			assertProblem(t, f.send(method, r.path, "Bearer "+ticket), http.StatusUnauthorized)
		}
	}
	if len(f.reached) != 0 {
		t.Errorf("handlers ran: %v", f.reached)
	}
}

// A route whose path names no session can't be a ticket's: it refuses every one.
func TestATicketOnARouteNamingNoSessionIsRefused(t *testing.T) {
	f := newFixture(t)
	f.rt.HandleTicket("GET /v1/probe", api.RequestTimeout, func(http.ResponseWriter, *http.Request, access.Caller) { t.Error("the handler ran") })
	assertProblem(t, f.send("GET", "/v1/probe", "Bearer "+f.ticket(t, ticketCaller)), http.StatusNotFound)
}
