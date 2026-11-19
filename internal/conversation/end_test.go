package conversation_test

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/conversation"
)

// sessionIdle is how long a session in these tests goes with no turn before it ends.
const sessionIdle = 5 * time.Minute

func TestEndingASessionMarksItEndedAndEndingItAgainKeepsTheFirstTime(t *testing.T) {
	h := newServiceOverFakes(t)
	clock := &steppedClock{now: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	h.sessions.SetClock(clock.Now)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())

	clock.step(time.Minute)
	ended, err := h.svc.EndSession(context.Background(), caller, s.ID)
	if err != nil || !ended.Ended() || !ended.EndedAt.Equal(clock.Now()) || ended.ID != s.ID {
		t.Fatalf("EndSession = %+v, %v; want the session ended now", ended, err)
	}
	clock.step(time.Minute)
	again, err := h.svc.EndSession(context.Background(), caller, s.ID)
	if err != nil || !again.EndedAt.Equal(ended.EndedAt) {
		t.Errorf("EndSession again = %+v, %v; want it ended at %s, as first", again, err, ended.EndedAt)
	}
	if read, err := h.svc.Session(context.Background(), caller, s.ID); err != nil || !read.EndedAt.Equal(ended.EndedAt) {
		t.Errorf("Session = %+v, %v; want it read back ended", read, err)
	}
}

// A product's server names no user, and may end any session of its workspace:
// its way to stop a session whose ticket was stolen.
func TestAProductEndsAnySessionOfItsWorkspaceAndNoOther(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	other := access.Caller{Workspace: uuid.MustParse("01a1de8a-0000-7000-8000-000000000002"), Product: true}
	if _, err := h.svc.EndSession(context.Background(), other, s.ID); !errors.Is(err, conversation.ErrNoSuchSession) {
		t.Errorf("EndSession by another workspace's product = %v, want ErrNoSuchSession", err)
	}
	ended, err := h.svc.EndSession(context.Background(), access.Caller{Workspace: workspace, Product: true}, s.ID)
	if err != nil || !ended.Ended() || ended.ID != s.ID {
		t.Errorf("EndSession by its workspace's product = %+v, %v; want the session ended", ended, err)
	}
}

func TestOnlyTheCallersOwnSessionCanBeEnded(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	tests := map[string]struct {
		c  access.Caller
		id uuid.UUID
	}{
		"another user":       {access.Caller{Workspace: workspace, User: uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a2")}, s.ID},
		"another workspace":  {access.Caller{Workspace: uuid.MustParse("01a1de8a-0000-7000-8000-000000000002"), User: user}, s.ID},
		"an unknown session": {caller, uuid.NewV7()},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := h.svc.EndSession(context.Background(), tt.c, tt.id); !errors.Is(err, conversation.ErrNoSuchSession) {
				t.Errorf("EndSession = %v, want ErrNoSuchSession", err)
			}
		})
	}
	if read, _ := h.svc.Session(context.Background(), caller, s.ID); read.Ended() {
		t.Errorf("the session ended at %s; want it live", read.EndedAt)
	}
}

func TestATurnInAnEndedSessionIsRefusedBeforeTheModelIsAsked(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	if _, err := h.svc.EndSession(context.Background(), caller, s.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	_, err := h.svc.TakeTurn(context.Background(), caller, turnIn(s.ID, "Is the lamp lit?"), noText(t))
	if asked, _ := h.model.Requests(); !errors.Is(err, conversation.ErrSessionEnded) || len(asked) != 0 || len(h.turns.All()) != 0 {
		t.Errorf("TakeTurn = %v, model asked %d times, saved %+v; want ErrSessionEnded with nothing asked or saved", err, len(asked), h.turns.All())
	}
}

func TestATurnInFlightWhenItsSessionEndsFinishesAndIsSaved(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	var endErr error
	turn, err := h.svc.TakeTurn(context.Background(), caller, turnIn(s.ID, "Is the lamp lit?"), func(text string) {
		if text == replyPieces[0] { // the turn is in flight while its text is sent
			_, endErr = h.svc.EndSession(context.Background(), caller, s.ID)
		}
	})
	saved := h.turns.All()
	if err != nil || endErr != nil || len(saved) != 1 || saved[0].Status != conversation.TurnAnswered || saved[0].Reply != reply || turn.ID != saved[0].ID {
		t.Errorf("TakeTurn = %+v, %v; EndSession = %v; saved %+v; want the turn answered and saved whole, the session ended", turn, err, endErr, saved)
	}
}

func TestATurnStartingAfterItsSessionEndedIsRefusedByTheStore(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	// The end lands after the turn read its session live, before the turn starts.
	if _, err := h.svc.EndSession(context.Background(), caller, s.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	_, err := h.turns.Start(context.Background(), conversation.Turn{ID: uuid.NewV7(), Workspace: workspace, User: user, Session: s.ID, RequestID: uuid.NewV7(), Words: "Is the lamp lit?"}, 0, time.Minute)
	if !errors.Is(err, conversation.ErrSessionEnded) {
		t.Errorf("Start = %v, want ErrSessionEnded", err)
	}
}

func TestASessionWithNoTurnForTheIdleTimeEnds(t *testing.T) {
	h := newServiceOverFakes(t)
	start := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	clock := &steppedClock{now: start}
	h.sessions.SetClock(clock.Now)
	quiet, _ := h.start(t, caller, keeperID, uuid.NewV7())
	talking, _ := h.start(t, caller, captainID, uuid.NewV7())
	clock.step(2 * time.Minute)
	h.takeTurn(t, turnIn(talking.ID, "Is the lamp lit?")) // starts and finishes at once on this clock

	for _, tt := range []struct {
		after                 time.Duration
		ended                 int
		quietEnded, talkEnded bool
	}{
		{sessionIdle - time.Second, 0, false, false},
		{sessionIdle, 1, true, false},
		{2*time.Minute + sessionIdle - time.Second, 0, true, false},
		{2*time.Minute + sessionIdle, 1, true, true},
		{time.Hour, 0, true, true},
	} {
		clock.step(start.Add(tt.after).Sub(clock.Now()))
		n, err := h.svc.EndIdle(context.Background())
		q, _ := h.svc.Session(context.Background(), caller, quiet.ID)
		k, _ := h.svc.Session(context.Background(), caller, talking.ID)
		if err != nil || n != tt.ended || q.Ended() != tt.quietEnded || k.Ended() != tt.talkEnded {
			t.Errorf("%s in: EndIdle = %d, %v; the quiet session ended %v, the talking one %v; want %d ended, %v, %v",
				tt.after, n, err, q.Ended(), k.Ended(), tt.ended, tt.quietEnded, tt.talkEnded)
		}
	}
}

func TestAUserHeardSpeakingKeepsTheSessionFromEndingIdle(t *testing.T) {
	h := newServiceOverFakes(t)
	start := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	clock := &steppedClock{now: start}
	h.sessions.SetClock(clock.Now)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())

	clock.step(4*time.Minute + 30*time.Second) // a long think, then an answer begins
	if err := h.svc.Active(context.Background(), caller, s.ID); err != nil {
		t.Fatalf("Active: %v", err)
	}
	clock.step(time.Minute)
	if n, _ := h.svc.EndIdle(context.Background()); n != 0 {
		t.Errorf("EndIdle ended %d sessions a minute after the user was heard, want none", n)
	}
	clock.step(sessionIdle)
	if n, _ := h.svc.EndIdle(context.Background()); n != 1 {
		t.Fatalf("EndIdle ended %d sessions the idle time after the user was heard, want 1", n)
	}
	if err := h.svc.Active(context.Background(), caller, s.ID); !errors.Is(err, conversation.ErrSessionEnded) {
		t.Errorf("Active once ended = %v, want ErrSessionEnded", err)
	}
}

func TestARetryOfATurnTakenJustBeforeTheEndIsARepeatNotARefusal(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	taken := turnIn(s.ID, "Is the lamp lit?")
	h.takeTurn(t, taken)
	if _, err := h.svc.EndSession(context.Background(), caller, s.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if _, err := h.svc.TakeTurn(context.Background(), caller, taken, noText(t)); !errors.Is(err, conversation.ErrTurnRepeated) {
		t.Errorf("the same request again = %v, want ErrTurnRepeated, so the caller counts it as taken", err)
	}
	if _, err := h.svc.TakeTurn(context.Background(), caller, turnIn(s.ID, "And the fog?"), noText(t)); !errors.Is(err, conversation.ErrSessionEnded) {
		t.Errorf("a new request = %v, want ErrSessionEnded", err)
	}
}

func TestARetryOfATurnStillInFlightWhenTheSessionEndedIsAnsweredAsBusy(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	first := turnIn(s.ID, "Is the lamp lit?")
	var retried error
	once := false
	if _, err := h.svc.TakeTurn(context.Background(), caller, first, func(string) {
		if once {
			return
		}
		once = true // the turn is in flight while its text is sent
		if _, err := h.svc.EndSession(context.Background(), caller, s.ID); err != nil {
			t.Errorf("EndSession: %v", err)
		}
		_, retried = h.svc.TakeTurn(context.Background(), caller, first, noText(t))
	}); err != nil {
		t.Fatalf("TakeTurn: %v", err)
	}
	if !errors.Is(retried, conversation.ErrTurnInFlight) {
		t.Errorf("the same request while its turn ran = %v, want ErrTurnInFlight, as for any repeat of a turn in flight", retried)
	}
}
