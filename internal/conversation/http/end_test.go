package http_test

import (
	nethttp "net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
)

func TestEndingASessionAnswers200WithItEndedAndTheSameAgain(t *testing.T) {
	f := newFixture(t)
	started, _ := f.startFor(t, "aiko-42")
	bearer := started.Ticket
	if started.EndedAt != nil {
		t.Errorf("a new session has ended_at %s, want none while it is live", started.EndedAt)
	}

	first := f.do("POST", "/v1/sessions/"+started.ID+"/end", bearer)
	if first.Code != nethttp.StatusOK {
		t.Fatalf("status = %d, want 200: %s", first.Code, first.Body)
	}
	ended := decode[sessionReply](t, first)
	if ended.ID != started.ID || ended.EndedAt == nil || ended.EndedAt.Before(started.StartedAt) {
		t.Errorf("end = %+v, want the session with when it ended", ended)
	}
	again := f.do("POST", "/v1/sessions/"+started.ID+"/end", bearer)
	if repeat := decode[sessionReply](t, again); again.Code != nethttp.StatusOK || repeat.EndedAt == nil || !repeat.EndedAt.Equal(*ended.EndedAt) {
		t.Errorf("end again = %d %+v, want 200 with the same end time", again.Code, repeat)
	}
}

func TestOnlyTheCallersOwnSessionCanBeEndedOverHTTP(t *testing.T) {
	f := newFixture(t)
	started, owner := f.startFor(t, "aiko-42")
	for name, tt := range map[string]struct {
		c    access.Caller
		path string
	}{
		"another user's": {forUser(owner, uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a2")), started.ID},
		"an unknown one": {owner, uuid.NewV7().String()},
	} {
		t.Run(name, func(t *testing.T) {
			assertProblem(t, f.do("POST", "/v1/sessions/"+tt.path+"/end", f.ticketFor(t, tt.c, tt.path)), nethttp.StatusNotFound)
		})
	}
	// The router answers a ticket on such a path; only the product's key reaches the handler.
	assertProblem(t, f.do("POST", "/v1/sessions/lamp/end", f.productKey(t)), nethttp.StatusNotFound)
}

func TestATurnInAnEndedSessionIsRefusedWithAProblemOfItsOwn(t *testing.T) {
	f := withSession(t, pieces("Bearing 040."), time.Minute)
	if rec := f.do("POST", "/v1/sessions/"+f.session+"/end", f.own); rec.Code != nethttp.StatusOK {
		t.Fatalf("end: status %d: %s", rec.Code, rec.Body)
	}
	p := assertProblemType(t, f.turn(t, f.owner, turnBody("Is the lamp lit?")), nethttp.StatusConflict, "/v1/problems/session-ended")
	if !strings.Contains(p.Detail, "ended") || len(f.turns.All()) != 0 {
		t.Errorf("problem %+v with turns saved %+v; want it to say the session ended, nothing saved", p, f.turns.All())
	}
}

func TestARetryOfATurnTakenBeforeTheEndIsRefusedAsARepeat(t *testing.T) {
	f := withSession(t, pieces("Bearing 040."), time.Minute)
	body := turnBody("Is the lamp lit?")
	if rec := f.turn(t, f.owner, body); rec.Code != nethttp.StatusOK {
		t.Fatalf("turn: status %d: %s", rec.Code, rec.Body)
	}
	if rec := f.do("POST", "/v1/sessions/"+f.session+"/end", f.own); rec.Code != nethttp.StatusOK {
		t.Fatalf("end: status %d: %s", rec.Code, rec.Body)
	}
	assertProblemType(t, f.turn(t, f.owner, body), nethttp.StatusConflict, "/v1/problems/turn-repeated")
}
