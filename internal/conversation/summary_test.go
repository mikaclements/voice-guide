package conversation_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/usercontext"
)

const instruction = "Note the questions covered and the weak spots."

// summarizing is the service over fakes with both agents asking for a summary and reading their
// own, and a session with the keeper in which the user took a turn, then ended.
func summarizing(t *testing.T) (serviceOverFakes, conversation.Session) {
	t.Helper()
	h := newServiceOverFakes(t)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea.", Reads: []string{usercontext.KindSessionSummary}, Summary: instruction})
	h.publish(t, agents.Agent{ID: captainID, Name: "Ferry captain", Persona: "You know every crossing by heart.", Reads: []string{usercontext.KindSessionSummary}, Summary: instruction})
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	h.takeTurn(t, turnIn(s.ID, "Is the lamp lit?"))
	if _, err := h.svc.EndSession(context.Background(), caller, s.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	return h, s
}

// summaries returns the summaries an agent's next session with the user would load, but the one
// the harness holds from the start.
func (h serviceOverFakes) summaries(t *testing.T, agent uuid.UUID) []string {
	t.Helper()
	loaded, err := usercontext.NewService(h.items).Load(context.Background(), usercontext.LoadRequest{
		Workspace: workspace, User: user, Agent: agent, Kinds: []string{usercontext.KindSessionSummary}, MaxChars: 10_000,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var out []string
	for _, it := range loaded.Items {
		if it.ID != summary.ID {
			out = append(out, it.Content)
		}
	}
	return out
}

func TestEndingASessionQueuesItsSummaryOnceAndAnIdleEndDoesToo(t *testing.T) {
	h := newServiceOverFakes(t)
	clock := &steppedClock{now: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	h.sessions.SetClock(clock.Now)
	ended, _ := h.start(t, caller, keeperID, uuid.NewV7())
	idle, _ := h.start(t, caller, captainID, uuid.NewV7())
	for range 2 {
		if _, err := h.svc.EndSession(context.Background(), caller, ended.ID); err != nil {
			t.Fatalf("EndSession: %v", err)
		}
	}
	clock.step(sessionIdle)
	if _, err := h.svc.EndIdle(context.Background()); err != nil {
		t.Fatalf("EndIdle: %v", err)
	}
	if got := h.sessions.QueuedSummaries(); !slices.Equal(got, []uuid.UUID{ended.ID, idle.ID}) {
		t.Errorf("summaries queued for %v, want one for the session ended, then one for the idle one", got)
	}
}

func TestASummaryIsWrittenFromTheTurnsWithTheAgentsInstructionOutsideAnySession(t *testing.T) {
	h, s := summarizing(t)
	h.model.Pieces = []string{"Covered the lamp. ", "Weak on bearings."}
	if err := h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID}); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	asked, _ := h.model.Requests()
	last := asked[len(asked)-1]
	if last.Session != uuid.Nil() || !strings.Contains(last.NewWords, "User: Is the lamp lit?") || !strings.Contains(last.NewWords, "Agent: "+reply) ||
		!strings.Contains(last.NewWords, instruction) || len(last.Conversation) != 0 {
		t.Errorf("asked %+v, want the transcript and the instruction, outside any session", last)
	}
	if got := h.summaries(t, keeperID); !slices.Equal(got, []string{"Covered the lamp. Weak on bearings."}) {
		t.Errorf("the keeper would load %q, want the summary written", got)
	}
	if got := h.summaries(t, captainID); len(got) != 0 {
		t.Errorf("the captain would load %q, want none: the summary is the keeper's", got)
	}
}

func TestTheNextSessionWithTheAgentLoadsItsSummary(t *testing.T) {
	h, s := summarizing(t)
	h.model.Pieces = []string{"Covered the lamp."}
	if err := h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID}); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	next, _ := h.start(t, caller, keeperID, uuid.NewV7())
	other, _ := h.start(t, caller, captainID, uuid.NewV7())
	// The keeper's: the summary written, then the one the harness holds from the start.
	if len(next.ContextItems) != 2 || next.ContextItems[1] != summary.ID || len(other.ContextItems) != 0 {
		t.Errorf("the keeper's next session loaded %v and the captain's %v; want the new summary for the keeper only", next.ContextItems, other.ContextItems)
	}
}

func TestAnInterruptedReplyIsInTheTranscriptAsFarAsItWasHeard(t *testing.T) {
	h, s := summarizing(t)
	turns := h.turns.All()
	if _, err := h.svc.Interrupted(context.Background(), turns[0], len("  **Bearing")); err != nil {
		t.Fatalf("Interrupted: %v", err)
	}
	if err := h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID}); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	asked, _ := h.model.Requests()
	if words := asked[len(asked)-1].NewWords; !strings.Contains(words, "Agent:   **Bearing—") || strings.Contains(words, "040") {
		t.Errorf("asked with %q, want the reply only as far as it was heard", words)
	}
}

func TestAnOpeningIsInTheTranscriptAsTheAgentsLineWithNoUserLineBeforeIt(t *testing.T) {
	h := newServiceOverFakes(t)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea.", Summary: instruction, Opens: true})
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	h.takeTurn(t, openingIn(s.ID))
	h.takeTurn(t, turnIn(s.ID, "Is the lamp lit?"))
	if _, err := h.svc.EndSession(context.Background(), caller, s.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if err := h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID}); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	asked, _ := h.model.Requests()
	if words := asked[len(asked)-1].NewWords; !strings.HasPrefix(words, "<transcript>\nAgent: "+reply+"\n\nUser: Is the lamp lit?") {
		t.Errorf("asked with %q, want the greeting first, with no user line before it", words)
	}
}

func TestASummaryRunTwiceIsSavedOnce(t *testing.T) {
	h, s := summarizing(t)
	for _, written := range []string{"First.", "Second."} {
		h.model.Pieces = []string{written}
		if err := h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID}); err != nil {
			t.Fatalf("Summarize: %v", err)
		}
	}
	if got := h.summaries(t, keeperID); !slices.Equal(got, []string{"First."}) {
		t.Errorf("the keeper would load %q, want the first summary only", got)
	}
}

func TestNoSummaryIsWrittenWhenThereIsNothingToSummarize(t *testing.T) {
	tests := map[string]func(h serviceOverFakes) uuid.UUID{
		"the user said nothing": func(h serviceOverFakes) uuid.UUID {
			s, _ := h.start(t, caller, keeperID, uuid.NewV7())
			_, _ = h.svc.EndSession(context.Background(), caller, s.ID)
			return s.ID
		},
		"only the agent spoke, opening it": func(h serviceOverFakes) uuid.UUID {
			h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea.", Reads: []string{usercontext.KindSessionSummary}, Summary: instruction, Opens: true})
			s, _ := h.start(t, caller, keeperID, uuid.NewV7())
			open := openingIn(s.ID)
			open.ScreenInput = []conversation.ScreenInputBlock{{Name: "chart", Content: "Rocks to the north."}}
			h.takeTurn(t, open)
			_, _ = h.svc.EndSession(context.Background(), caller, s.ID)
			return s.ID
		},
		"the session never ended": func(h serviceOverFakes) uuid.UUID {
			s, _ := h.start(t, caller, keeperID, uuid.NewV7())
			h.takeTurn(t, turnIn(s.ID, "Is the lamp lit?"))
			return s.ID
		},
		"no such session": func(serviceOverFakes) uuid.UUID { return uuid.NewV7() },
		"the agent asks for none": func(h serviceOverFakes) uuid.UUID {
			h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You keep the lamp lit."})
			s, _ := h.start(t, caller, keeperID, uuid.NewV7())
			h.takeTurn(t, turnIn(s.ID, "Is the lamp lit?"))
			_, _ = h.svc.EndSession(context.Background(), caller, s.ID)
			return s.ID
		},
	}
	for name, session := range tests {
		t.Run(name, func(t *testing.T) {
			h := newServiceOverFakes(t)
			h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea.", Reads: []string{usercontext.KindSessionSummary}, Summary: instruction})
			id := session(h)
			before, _ := h.model.Requests()
			if err := h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: id}); err != nil {
				t.Errorf("Summarize = %v, want done with nothing to write", err)
			}
			if after, _ := h.model.Requests(); len(after) != len(before) || len(h.summaries(t, keeperID)) != 0 {
				t.Errorf("the model was asked %d more times and summaries %q saved; want neither", len(after)-len(before), h.summaries(t, keeperID))
			}
		})
	}
}

func TestASummaryFailsToRunAgainWhileATurnIsInFlightOrTheModelFails(t *testing.T) {
	t.Run("the model fails", func(t *testing.T) {
		h, s := summarizing(t)
		h.model.Fail = "overloaded"
		err := h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID})
		var failed *conversation.ReplyFailedError
		if !errors.As(err, &failed) || len(h.summaries(t, keeperID)) != 0 {
			t.Errorf("Summarize = %v, want the model's failure, nothing saved", err)
		}
	})
	t.Run("a turn is in flight", func(t *testing.T) {
		h := newServiceOverFakes(t)
		h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea.", Summary: instruction})
		s, _ := h.start(t, caller, keeperID, uuid.NewV7())
		var summarized error
		h.takeTurnWith(t, turnIn(s.ID, "Is the lamp lit?"), func() {
			_, _ = h.svc.EndSession(context.Background(), caller, s.ID)
			summarized = h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID})
		})
		if summarized == nil || len(h.summaries(t, keeperID)) != 0 {
			t.Errorf("Summarize while the turn ran = %v, want a failure to run again, nothing saved", summarized)
		}
	})
}

func TestASummaryOverTheCapIsCutAtTheLastSentenceWithin(t *testing.T) {
	h, s := summarizing(t)
	sentence := strings.Repeat("a", 99) + ". " // 101 characters
	h.model.Pieces = []string{strings.Repeat(sentence, 25)}
	if err := h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID}); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	got := h.summaries(t, keeperID)
	if len(got) != 1 || !strings.HasSuffix(got[0], ".") || len(got[0]) > 2000 || len(got[0]) < 1900 {
		t.Errorf("saved %d characters, ending %q; want under 2,000, cut after a sentence", len(got[0]), got[0][len(got[0])-3:])
	}
}

// takeTurnWith takes the turn, calling during once while its reply is being sent, the turn in flight.
func (h serviceOverFakes) takeTurnWith(t *testing.T, r conversation.TurnRequest, during func()) {
	t.Helper()
	once := false
	if _, err := h.svc.TakeTurn(context.Background(), caller, r, func(string) {
		if !once {
			once = true
			during()
		}
	}); err != nil {
		t.Fatalf("TakeTurn: %v", err)
	}
}

// orderedTurns is the fake store of turns, noting the order the summary reads and writes in.
type orderedTurns struct {
	conversation.Turns
	order *[]string
}

func (o orderedTurns) CutOff(ctx context.Context, ws, session uuid.UUID, d time.Duration) error {
	*o.order = append(*o.order, "cut off")
	return o.Turns.CutOff(ctx, ws, session, d)
}

func (o orderedTurns) Finished(ctx context.Context, ws, session uuid.UUID) ([]conversation.Turn, error) {
	*o.order = append(*o.order, "finished")
	return o.Turns.Finished(ctx, ws, session)
}

func TestASummarySavesATurnCutOffLostBeforeReadingTheTurns(t *testing.T) {
	h, s := summarizing(t)
	var order []string
	svc := conversation.NewService(agents.NewCatalog(h.agents), h.sessions, orderedTurns{Turns: h.turns, order: &order}, h.model, usercontext.NewService(h.items), h.settings)
	if err := svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID}); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if !slices.Equal(order, []string{"cut off", "finished"}) {
		t.Errorf("the summary did %v, want a turn cut off saved lost before the turns are read", order)
	}
}

func TestTheLatestSessionsSummaryComesFirstWithItsDateEvenWhenSavedFirst(t *testing.T) {
	h := newServiceOverFakes(t)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea.", Reads: []string{usercontext.KindSessionSummary}, Summary: instruction})
	clock := &steppedClock{now: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	h.sessions.SetClock(clock.Now)
	var ended []conversation.Session
	for _, said := range []string{"Is the lamp lit?", "And the fog?"} {
		s, _ := h.start(t, caller, keeperID, uuid.NewV7())
		h.takeTurn(t, turnIn(s.ID, said))
		clock.step(time.Hour)
		if _, err := h.svc.EndSession(context.Background(), caller, s.ID); err != nil {
			t.Fatalf("EndSession: %v", err)
		}
		ended = append(ended, s)
	}
	// The later session's summary is saved first, the earlier one's after it, as a retried job would.
	for i, written := range map[int]string{1: "Covered the fog.", 0: "Covered the lamp."} {
		h.model.Pieces = []string{written}
		if err := h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: ended[i].ID}); err != nil {
			t.Fatalf("Summarize: %v", err)
		}
	}
	if got := h.summaries(t, keeperID); !slices.Equal(got, []string{"Covered the fog.", "Covered the lamp."}) {
		t.Fatalf("the keeper would load %q, want the latest session's summary first", got)
	}
	next, _ := h.start(t, caller, keeperID, uuid.NewV7())
	h.takeTurn(t, turnIn(next.ID, "Hi."))
	asked, _ := h.model.Requests()
	layers := strings.Join(asked[len(asked)-1].StableLayers, "\n")
	fog, lamp := strings.Index(layers, `<session_summary ended="2026-11-18 11:00 UTC">`), strings.Index(layers, `<session_summary ended="2026-11-18 10:00 UTC">`)
	if fog < 0 || lamp < 0 || fog > lamp {
		t.Errorf("the next session's layers:\n%s\nwant each summary dated by its session's end, the latest first", layers)
	}
}

// failingSummaries is user context's fake store, failing every summary save.
type failingSummaries struct{ *countingItems }

func (failingSummaries) AddSummary(context.Context, usercontext.Item) (usercontext.Item, bool, error) {
	return usercontext.Item{}, false, errors.New("connection refused by 10.0.0.7:5432")
}

func TestASummaryUserContextCantSaveFailsToRunAgain(t *testing.T) {
	h, s := summarizing(t)
	svc := conversation.NewService(agents.NewCatalog(h.agents), h.sessions, h.turns, h.model, usercontext.NewService(failingSummaries{h.items}), h.settings)
	if err := svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID}); err == nil {
		t.Error("Summarize = nil with user context unable to save, want a failure, so the job runs again")
	}
}

func TestOnlyTheModelServiceAtItsCapMakesASummaryBusy(t *testing.T) {
	for kind, busy := range map[conversation.ModelErrorKind]bool{"busy": true, "overloaded": false, "unavailable": false} {
		t.Run(string(kind), func(t *testing.T) {
			h, s := summarizing(t)
			h.model.Fail, h.model.Pieces = kind, nil
			err := h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID})
			if err == nil || errors.Is(err, conversation.ErrModelBusy) != busy {
				t.Errorf("Summarize = %v; want busy %v: only the model service's own cap lets the job go uncounted", err, busy)
			}
		})
	}
}

func TestASummaryAsksForTheModelOfItsSessionsAgentVersion(t *testing.T) {
	for _, tt := range []struct{ named, want string }{{"claude-sonnet-5-5", "claude-sonnet-5-5"}, {"", "claude-test"}} {
		h := newServiceOverFakes(t)
		h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea.", Summary: instruction, Model: tt.named})
		s, _ := h.start(t, caller, keeperID, uuid.NewV7())
		h.takeTurn(t, turnIn(s.ID, "Is the lamp lit?"))
		if _, err := h.svc.EndSession(context.Background(), caller, s.ID); err != nil {
			t.Fatalf("EndSession: %v", err)
		}
		if err := h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID}); err != nil {
			t.Fatalf("Summarize: %v", err)
		}
		asked, _ := h.model.Requests()
		if len(asked) != 2 || asked[0].Model != tt.want || asked[1].Model != tt.want {
			t.Errorf("with model %q the model was asked %+v, want %q for the turn and the summary", tt.named, asked, tt.want)
		}
	}
}

func TestASummaryIsWrittenOnTheSummarysOwnModelWhenItsSectionNamesOne(t *testing.T) {
	for _, tt := range []struct{ agentModel, summaryModel, wantTurn, wantSummary string }{
		{"claude-sonnet-5-5", "claude-opus-5-5", "claude-sonnet-5-5", "claude-opus-5-5"},
		{"", "claude-opus-5-5", "claude-test", "claude-opus-5-5"},
		{"claude-sonnet-5-5", "", "claude-sonnet-5-5", "claude-sonnet-5-5"},
	} {
		h := newServiceOverFakes(t)
		h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea.", Summary: instruction, Model: tt.agentModel, SummaryModel: tt.summaryModel})
		s, _ := h.start(t, caller, keeperID, uuid.NewV7())
		h.takeTurn(t, turnIn(s.ID, "Is the lamp lit?"))
		if _, err := h.svc.EndSession(context.Background(), caller, s.ID); err != nil {
			t.Fatalf("EndSession: %v", err)
		}
		if err := h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID}); err != nil {
			t.Fatalf("Summarize: %v", err)
		}
		asked, _ := h.model.Requests()
		if len(asked) != 2 || asked[0].Model != tt.wantTurn || asked[1].Model != tt.wantSummary {
			t.Errorf("with models %q and %q the model was asked %+v, want %q for the turn and %q for the summary", tt.agentModel, tt.summaryModel, asked, tt.wantTurn, tt.wantSummary)
		}
	}
}
