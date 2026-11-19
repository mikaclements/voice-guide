package conversation_test

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/usercontext"
)

// held takes r as a turn held in flight once its reply ends.
func (h serviceOverFakes) held(t *testing.T, r conversation.TurnRequest) conversation.Turn {
	t.Helper()
	r.Hold = true
	turn, err := h.svc.TakeTurn(context.Background(), caller, r, func(string) {})
	if err != nil {
		t.Fatalf("TakeTurn: %v", err)
	}
	return turn
}

func TestAHeldTurnStaysInFlightUntilSettled(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	turn := h.held(t, turnIn(s.ID, "Where is the ferry?"))
	if saved := h.turns.All(); len(saved) != 1 || saved[0].Status != conversation.TurnInFlight || saved[0].Reply != "" {
		t.Fatalf("saved %+v, want the turn in flight, its reply not yet saved", saved)
	}
	if _, err := h.svc.TakeTurn(context.Background(), caller, turnIn(s.ID, "Is the lamp lit?"), noText(t)); !errors.Is(err, conversation.ErrTurnInFlight) {
		t.Errorf("another turn meanwhile = %v, want ErrTurnInFlight", err)
	}

	settled, err := h.svc.Settle(context.Background(), turn)
	if saved := h.turns.All(); err != nil || saved[0].Status != conversation.TurnAnswered || saved[0].Reply != reply || settled.Stages[len(settled.Stages)-1].Name != conversation.StageSave {
		t.Errorf("Settle = %+v, %v; saved %+v; want the turn answered with its reply, its save timed", settled, err, saved)
	}
}

func TestATurnTakenBackIsRemovedAndAllTheWordsTakeItsPlace(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	first := turnIn(s.ID, "I led the rebuild of shipment tracking:")
	if err := h.svc.TakeBack(context.Background(), h.held(t, first)); err != nil {
		t.Fatalf("TakeBack: %v", err)
	}
	if saved := h.turns.All(); len(saved) != 0 {
		t.Fatalf("saved %+v, want the turn taken back removed", saved)
	}

	merged := first
	merged.Words = "I led the rebuild of shipment tracking: status updates moved to an event stream."
	h.takeTurn(t, merged)
	asked, _ := h.model.Requests()
	if saved := h.turns.All(); len(saved) != 1 || saved[0].Seq != 1 || saved[0].Words != merged.Words || len(asked[1].Conversation) != 0 {
		t.Errorf("saved %+v, asked with %+v; want one turn with all the words, its prompt holding nothing taken back", saved, asked[1].Conversation)
	}
}

func TestOnlyATurnStillInFlightCanBeTakenBack(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	turn := h.held(t, turnIn(s.ID, "Where is the ferry?"))
	if _, err := h.svc.Settle(context.Background(), turn); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if err := h.svc.TakeBack(context.Background(), turn); err == nil || len(h.turns.All()) != 1 {
		t.Errorf("TakeBack of a settled turn = %v, saved %+v; want it refused and the turn kept", err, h.turns.All())
	}
}

// A turn typed over HTTP can't start while a spoken turn is held, and once the spoken turn is taken
// back it takes the number freely, since no finished turn was removed: the user's words, all of
// them, then follow it, their prompt holding it.
func TestATurnRacingATakeBackComesBeforeAllTheWordsNeverInPlaceOfAFinishedTurn(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	spoken := turnIn(s.ID, "I led the rebuild:")
	held := h.held(t, spoken)
	if _, err := h.svc.TakeTurn(context.Background(), caller, turnIn(s.ID, "Is the lamp lit?"), noText(t)); !errors.Is(err, conversation.ErrTurnInFlight) {
		t.Fatalf("a typed turn while the spoken one is held = %v, want ErrTurnInFlight", err)
	}
	if err := h.svc.TakeBack(context.Background(), held); err != nil {
		t.Fatalf("TakeBack: %v", err)
	}
	typed, _ := h.takeTurn(t, turnIn(s.ID, "Is the lamp lit?"))
	spoken.Words = "I led the rebuild: status moved to events."
	merged, _ := h.takeTurn(t, spoken)
	asked, _ := h.model.Requests()
	if typed.Seq != 1 || merged.Seq != 2 || len(asked[len(asked)-1].Conversation) != 2 {
		t.Errorf("numbered %d then %d, last asked with %+v; want the typed turn first and all the words after it, holding it", typed.Seq, merged.Seq, asked[len(asked)-1].Conversation)
	}
}

// A held turn is settled by its reply's deadline, and its save ends by the turn's, after which the
// session's next turn may start, however late the caller settles it.
func TestAHeldTurnIsSettledWithinTheTurnDeadline(t *testing.T) {
	h := newServiceOverFakes(t)
	h.settings.TurnDeadline = 200 * time.Millisecond // short, since the reply runs to its deadline
	const kept = 100 * time.Millisecond              // half of a deadline that short
	var replyBy time.Time
	var save saveTimes
	svc := conversation.NewService(agents.NewCatalog(h.agents), h.sessions, watchedSaves{Turns: h.turns, save: &save}, runsToItsDeadline{&replyBy}, usercontext.NewService(h.items), h.settings)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	r := turnIn(s.ID, "Is the lamp lit?")
	r.Hold = true
	turn, _ := svc.TakeTurn(context.Background(), caller, r, func(string) {})
	if !turn.SettleBy().Equal(replyBy) {
		t.Errorf("settle by %v, want the reply's deadline, %v", turn.SettleBy(), replyBy)
	}
	if _, err := svc.Settle(context.Background(), turn); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if save.by.IsZero() || save.by.After(replyBy.Add(kept)) {
		t.Errorf("the save's deadline was %v after the reply's; want at most %v, the turn's own", save.by.Sub(replyBy), kept)
	}
}
