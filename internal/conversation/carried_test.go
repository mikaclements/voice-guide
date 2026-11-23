package conversation_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/usercontext"
)

// keeperReadingSummaries is the service with the lighthouse keeper reading
// its session summaries, so its sessions carry the ones before them.
func keeperReadingSummaries(t *testing.T) serviceOverFakes {
	t.Helper()
	h := newServiceOverFakes(t)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Reads: []string{"session_summary"}, Persona: "You read every log."})
	return h
}

// stableLayers returns the stable layers of the model's last request.
func (h serviceOverFakes) stableLayers(t *testing.T) []string {
	t.Helper()
	asked, _ := h.model.Requests()
	if len(asked) == 0 {
		t.Fatal("the model was never asked")
	}
	return asked[len(asked)-1].StableLayers
}

func TestANewStartEndsTheUsersLiveSessionWithTheAgentAndQueuesItsSummary(t *testing.T) {
	h := newServiceOverFakes(t)
	old, _ := h.start(t, caller, keeperID, uuid.NewV7())

	started, isNew := h.start(t, caller, keeperID, uuid.NewV7())
	if got := h.read(t, old.ID); !isNew || !got.Ended() || got.SummaryStatus != conversation.SummaryPending {
		t.Errorf("the earlier session after a new start = %+v; want it ended, its summary pending", got)
	}
	if !slices.Equal(h.sessions.QueuedSummaries(), []uuid.UUID{old.ID}) || h.read(t, started.ID).Ended() {
		t.Errorf("summaries queued for %v, the new session ended %v; want one for the earlier session and the new one live", h.sessions.QueuedSummaries(), h.read(t, started.ID).Ended())
	}
}

func TestOnlyTheUsersOwnLiveSessionWithTheSameAgentEnds(t *testing.T) {
	h := newServiceOverFakes(t)
	other := access.Caller{Workspace: workspace, User: uuid.NewV7()}
	captains, _ := h.start(t, caller, captainID, uuid.NewV7())
	theirs, _ := h.start(t, other, keeperID, uuid.NewV7())

	h.start(t, caller, keeperID, uuid.NewV7())
	if h.read(t, captains.ID).Ended() {
		t.Error("the user's session with another agent ended, want it live")
	}
	if s, _, _ := h.sessions.ByID(context.Background(), workspace, other.User, theirs.ID); s.Ended() {
		t.Error("another user's session with the agent ended, want it live")
	}
}

func TestARepeatedStartEndsNothing(t *testing.T) {
	h := newServiceOverFakes(t)
	request := uuid.NewV7()
	first, _ := h.start(t, caller, keeperID, request)
	later, _ := h.start(t, caller, keeperID, uuid.NewV7()) // ends the first
	// Another instance's lookup misses the first, as for a repeat racing it.
	h.svc = conversation.NewService(agents.NewCatalog(h.agents), staleLookup{h.sessions}, h.turns, h.model, usercontext.NewService(h.items), h.settings)

	if again, isNew := h.start(t, caller, keeperID, request); isNew || again.ID != first.ID || h.read(t, later.ID).Ended() {
		t.Errorf("repeat = %+v, new %v; want the first session back and the later one still live", again, isNew)
	}
}

func TestANewSessionCarriesTheTurnsOfTheSessionItEndedInEveryPrompt(t *testing.T) {
	h := keeperReadingSummaries(t)
	old, _ := h.start(t, caller, keeperID, uuid.NewV7())
	h.takeTurn(t, turnIn(old.ID, "My boat is the Tern."))

	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	if !slices.Equal(s.CarriedSessions, []uuid.UUID{old.ID}) {
		t.Fatalf("carried sessions = %v, want the one it ended", s.CarriedSessions)
	}
	h.takeTurn(t, turnIn(s.ID, "Which boat is mine?"))
	first := h.stableLayers(t)
	if !strings.Contains(first[len(first)-1], `{"user":"My boat is the Tern.","agent":"  **Bearing 040.**\n\n- Check the lamp\n\n\n"}`) {
		t.Errorf("stable layers = %q; want the earlier session's turn in the last", first)
	}
	h.takeTurn(t, turnIn(s.ID, "And its berth?"))
	if again := h.stableLayers(t); !slices.Equal(again, first) {
		t.Errorf("the next turn's stable layers = %q; want them byte for byte as before, %q", again, first)
	}
}

func TestTheOpeningKnowsTheCarriedConversation(t *testing.T) {
	h := newServiceOverFakes(t)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Reads: []string{"session_summary"}, Persona: "You read every log.", Opens: true})
	old, _ := h.start(t, caller, keeperID, uuid.NewV7())
	h.takeTurn(t, turnIn(old.ID, "My boat is the Tern."))

	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	h.takeTurn(t, openingIn(s.ID))
	if layers := h.stableLayers(t); !strings.Contains(layers[len(layers)-1], "My boat is the Tern.") {
		t.Errorf("the opening's stable layers = %q; want the earlier session's turn in the last", layers)
	}
}

func TestAStartThatFailsEndsNothing(t *testing.T) {
	h := keeperReadingSummaries(t)
	old, _ := h.start(t, caller, keeperID, uuid.NewV7())
	h.items.err = errors.New("user context unreachable")

	if _, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7()}); err == nil {
		t.Fatal("StartSession succeeded without the user's context")
	}
	if h.read(t, old.ID).Ended() || len(h.sessions.QueuedSummaries()) != 0 {
		t.Errorf("after the failed start the earlier session ended %v, summaries queued %v; want it live and nothing queued", h.read(t, old.ID).Ended(), h.sessions.QueuedSummaries())
	}
}

func TestAnEndedSessionWhoseSummaryIsStillComingIsCarriedAndOneSettledIsNot(t *testing.T) {
	h := keeperReadingSummaries(t)
	settled, _ := h.start(t, caller, keeperID, uuid.NewV7())
	pending, _ := h.start(t, caller, keeperID, uuid.NewV7()) // ends the first
	if err := h.sessions.SettleSummary(context.Background(), workspace, user, settled.ID, conversation.SummaryWritten); err != nil {
		t.Fatalf("SettleSummary: %v", err)
	}
	if _, err := h.svc.EndSession(context.Background(), caller, pending.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}

	if s, _ := h.start(t, caller, keeperID, uuid.NewV7()); !slices.Equal(s.CarriedSessions, []uuid.UUID{pending.ID}) {
		t.Errorf("carried sessions = %v, want only the one whose summary is pending, %v", s.CarriedSessions, pending.ID)
	}
}

func TestAnAgentThatDoesntReadItsSummariesEndsTheLastSessionButCarriesNothing(t *testing.T) {
	h := newServiceOverFakes(t) // the ferry captain reads nothing
	old, _ := h.start(t, caller, captainID, uuid.NewV7())

	if s, _ := h.start(t, caller, captainID, uuid.NewV7()); len(s.CarriedSessions) != 0 || !h.read(t, old.ID).Ended() {
		t.Errorf("carried sessions = %v, earlier one ended %v; want none carried and it ended", s.CarriedSessions, h.read(t, old.ID).Ended())
	}
}

func TestAUsersFirstSessionCarriesNothing(t *testing.T) {
	h := keeperReadingSummaries(t)
	s, _ := h.startFor(t, "first-timer", uuid.NewV7())
	if len(s.CarriedSessions) != 0 {
		t.Errorf("carried sessions = %v, want none", s.CarriedSessions)
	}
}

func TestATurnInFlightInTheEndedSessionFinishesAndJoinsTheNextPrompt(t *testing.T) {
	h := keeperReadingSummaries(t)
	old, _ := h.start(t, caller, keeperID, uuid.NewV7())
	inFlight := h.held(t, turnIn(old.ID, "My boat is the Tern."))

	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	h.takeTurn(t, turnIn(s.ID, "Which boat is mine?"))
	if layers := h.stableLayers(t); slices.ContainsFunc(layers, func(l string) bool { return strings.Contains(l, "Tern") }) {
		t.Errorf("stable layers = %q; want no turn still in flight", layers)
	}
	if _, err := h.svc.Settle(context.Background(), inFlight); err != nil {
		t.Fatalf("Settle after the end: %v", err)
	}
	h.takeTurn(t, turnIn(s.ID, "Did you hear me?"))
	if layers := h.stableLayers(t); !strings.Contains(layers[len(layers)-1], "My boat is the Tern.") {
		t.Errorf("stable layers = %q; want the turn saved after the end in the last", layers)
	}
}
