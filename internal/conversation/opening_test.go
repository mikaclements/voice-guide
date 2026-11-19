package conversation_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/conversation"
	sessionsfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/usercontext"
)

// opening is the service over fakes with the keeper set to speak first, and a session with it.
func opening(t *testing.T) (serviceOverFakes, conversation.Session) {
	t.Helper()
	h := newServiceOverFakes(t)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea.", Opens: true})
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	return h, s
}

func openingIn(session uuid.UUID) conversation.TurnRequest {
	return conversation.TurnRequest{Session: session, RequestID: uuid.NewV7(), Opening: true}
}

func TestTheOpeningIsTurnOneWithNoWordsAndTheNextPromptContinuesItByteForByte(t *testing.T) {
	h, s := opening(t)
	open := openingIn(s.ID)
	turn, _ := h.takeTurn(t, open)
	want := conversation.Turn{ID: turn.ID, Workspace: workspace, User: user, Session: s.ID, RequestID: open.RequestID, Seq: 1, Reply: reply, Status: conversation.TurnAnswered, Opening: true}
	if saved := h.turns.All(); len(saved) != 1 || !reflect.DeepEqual(saved[0], want) {
		t.Errorf("saved %+v, want %+v", saved, want)
	}

	h.takeTurn(t, turnIn(s.ID, "Is the lamp lit?"))
	asked, _ := h.model.Requests()
	if len(asked) != 2 || asked[0].NewWords != conversation.OpeningCue || len(asked[0].Conversation) != 0 {
		t.Fatalf("model asked %+v, want the opening asked with the cue alone, then the turn", asked)
	}
	// The opening's request is the cache's warm-up: the next one starts with every byte of it.
	wantConversation := []conversation.Message{{Role: conversation.RoleUser, Text: conversation.OpeningCue}, {Role: conversation.RoleAgent, Text: reply}}
	if !reflect.DeepEqual(asked[1].StableLayers, asked[0].StableLayers) || !reflect.DeepEqual(asked[1].Conversation, wantConversation) {
		t.Errorf("the first turn was asked with %+v, want the opening's stable layers, then its cue and reply: %q", asked[1].Prompt, wantConversation)
	}
}

func TestAnOpeningIsRefusedBeforeTheModelIsAskedWhenItCantBeTaken(t *testing.T) {
	tests := map[string]struct {
		opens bool
		words string
		want  error
	}{
		"in a session whose agent waits for the user": {false, "", conversation.ErrOpeningRefused},
		"with words": {true, "Hello?", conversation.ErrWordsUnusable},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			h := newServiceOverFakes(t)
			h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea.", Opens: tt.opens})
			s, _ := h.start(t, caller, keeperID, uuid.NewV7())
			r := openingIn(s.ID)
			r.Words = tt.words
			if _, err := h.svc.TakeTurn(context.Background(), caller, r, noText(t)); !errors.Is(err, tt.want) {
				t.Errorf("TakeTurn = %v, want %v", err, tt.want)
			}
			if asked, _ := h.model.Requests(); len(h.turns.All())+len(asked) != 0 {
				t.Errorf("saved %+v and asked the model %+v; want nothing", h.turns.All(), asked)
			}
		})
	}
}

// A reload or a reconnect sends the opening again: the agent opened already, so nothing is said twice.
func TestAnOpeningOnceTheSessionHasATurnIsRefused(t *testing.T) {
	h, s := opening(t)
	h.takeTurn(t, openingIn(s.ID))
	if _, err := h.svc.TakeTurn(context.Background(), caller, openingIn(s.ID), noText(t)); !errors.Is(err, conversation.ErrOpeningRefused) {
		t.Errorf("a second opening = %v, want ErrOpeningRefused", err)
	}
	if asked, _ := h.model.Requests(); len(asked) != 1 || len(h.turns.All()) != 1 {
		t.Errorf("model asked %d times, %d turns saved; want the first opening's alone", len(asked), len(h.turns.All()))
	}
}

func TestAnOpeningAfterTheUserSpokeFirstIsRefusedAndTheirTurnAnswered(t *testing.T) {
	h, s := opening(t)
	var during error
	turn, err := h.svc.TakeTurn(context.Background(), caller, turnIn(s.ID, "Is the lamp lit?"), func(text string) {
		if text == replyPieces[0] { // the user's turn is in flight
			_, during = h.svc.TakeTurn(context.Background(), caller, openingIn(s.ID), noText(t))
		}
	})
	_, after := h.svc.TakeTurn(context.Background(), caller, openingIn(s.ID), noText(t))
	if err != nil || turn.Seq != 1 || turn.Reply != reply || !errors.Is(during, conversation.ErrOpeningRefused) || !errors.Is(after, conversation.ErrOpeningRefused) {
		t.Errorf("the user's turn = %+v, %v; openings during it %v and after %v; want it answered as turn 1 and both openings refused", turn, err, during, after)
	}
	// The one during the turn was refused only when its turn started, so its reply was asked for, then stopped.
	if asked, stopped := h.model.Requests(); len(asked) != 2 || stopped != 1 {
		t.Errorf("model asked %d times and stopped %d; want the turn and the opening during it asked, that opening stopped", len(asked), stopped)
	}
}

// bothRead holds each read of a session's turns until two have come, so two openings each find
// the session with none.
type bothRead struct {
	*sessionsfake.Turns
	read *sync.WaitGroup
}

func (b bothRead) Finished(ctx context.Context, workspace, session uuid.UUID) ([]conversation.Turn, error) {
	b.read.Done()
	b.read.Wait()
	return b.Turns.Finished(ctx, workspace, session)
}

// Two copies of voice, or two tabs, may send the opening at once.
func TestTwoOpeningsAtOnceTakeOneTurnAndRefuseTheOther(t *testing.T) {
	h, s := opening(t)
	read := &sync.WaitGroup{}
	read.Add(2)
	svc := conversation.NewService(agents.NewCatalog(h.agents), h.sessions, bothRead{h.turns, read}, h.model, usercontext.NewService(h.items), h.settings)
	errs := make([]error, 2)
	var both sync.WaitGroup
	for i := range errs {
		both.Go(func() { _, errs[i] = svc.TakeTurn(context.Background(), caller, openingIn(s.ID), func(string) {}) })
	}
	both.Wait()
	oneEach := errs[0] == nil && errors.Is(errs[1], conversation.ErrOpeningRefused) || errs[1] == nil && errors.Is(errs[0], conversation.ErrOpeningRefused)
	if saved := h.turns.All(); !oneEach || len(saved) != 1 || !saved[0].Opening {
		t.Errorf("TakeTurn = %v, saved %+v; want one opening taken as the only turn, the other refused", errs, saved)
	}
}

func TestAGreetingCutShortReachesTheNextPromptAsFarAsItWasHeard(t *testing.T) {
	h, s := opening(t)
	greeting, _ := h.takeTurn(t, openingIn(s.ID))
	if _, err := h.svc.Interrupted(context.Background(), greeting, len("  **Bearing")); err != nil {
		t.Fatalf("Interrupted: %v", err)
	}
	h.takeTurn(t, turnIn(s.ID, "Wait."))
	asked, _ := h.model.Requests()
	wantConversation := []conversation.Message{{Role: conversation.RoleUser, Text: conversation.OpeningCue}, {Role: conversation.RoleAgent, Text: "  **Bearing—"}}
	if p := asked[1].Prompt; !reflect.DeepEqual(p.Conversation, wantConversation) || !strings.HasSuffix(p.NewWords, "\n\nWait.") || p.NewWords == "Wait." {
		t.Errorf("the next turn was asked with %+v, want the greeting as far as heard, then the note that the user cut in and their words", p)
	}
}

func TestAnOpeningTheModelFailsIsSavedLostAndTheFirstTurnCarriesIt(t *testing.T) {
	h, s := opening(t)
	h.model.Fail, h.model.Pieces = "overloaded", nil // before its first word
	var failed *conversation.ReplyFailedError
	if _, err := h.svc.TakeTurn(context.Background(), caller, openingIn(s.ID), noText(t)); !errors.As(err, &failed) {
		t.Fatalf("the opening = %v, want its reply failed", err)
	}
	h.model.Fail, h.model.Pieces = "", replyPieces
	turn, _ := h.takeTurn(t, turnIn(s.ID, "Hello?"))
	asked, _ := h.model.Requests()
	if saved := h.turns.All(); saved[0].Status != conversation.TurnLost || !saved[0].Opening || turn.Seq != 2 || turn.Status != conversation.TurnAnswered {
		t.Errorf("saved %+v, want the opening lost and the user's turn answered after it", saved)
	}
	if want := []conversation.Message{{Role: conversation.RoleUser, Text: conversation.OpeningCue}}; !reflect.DeepEqual(asked[1].Conversation, want) {
		t.Errorf("the user's turn was asked with the conversation %+v, want the lost opening's cue: %+v", asked[1].Conversation, want)
	}
}

func TestAnOpeningSentAgainWithItsRequestIDIsAnsweredAsARepeat(t *testing.T) {
	h, s := opening(t)
	open := openingIn(s.ID)
	h.takeTurn(t, open)
	if _, err := h.svc.TakeTurn(context.Background(), caller, open, noText(t)); !errors.Is(err, conversation.ErrTurnRepeated) {
		t.Errorf("the opening again = %v, want ErrTurnRepeated", err)
	}
}

func TestAnOpeningAfterTheSessionEndedIsRefusedAsEnded(t *testing.T) {
	h, s := opening(t)
	if _, err := h.svc.EndSession(context.Background(), caller, s.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if _, err := h.svc.TakeTurn(context.Background(), caller, openingIn(s.ID), noText(t)); !errors.Is(err, conversation.ErrSessionEnded) {
		t.Errorf("TakeTurn = %v, want ErrSessionEnded", err)
	}
}
