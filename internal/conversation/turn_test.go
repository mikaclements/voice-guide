package conversation_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/conversation"
	sessionsfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/usercontext"
)

// The fake model's reply to every turn, in pieces, with the leading spaces,
// Markdown and trailing newlines a reply may have.
var replyPieces = []string{"  **Bearing", " 040.**\n\n", "- Check the lamp\n\n\n"}

const reply = "  **Bearing 040.**\n\n- Check the lamp\n\n\n"

func turnIn(session uuid.UUID, words string) conversation.TurnRequest {
	return conversation.TurnRequest{Session: session, RequestID: uuid.NewV7(), Words: words}
}

// takeTurn takes the turn for the caller and returns it with the text sent,
// checking that no text arrives before the turn is saved.
func (h serviceOverFakes) takeTurn(t *testing.T, r conversation.TurnRequest) (conversation.Turn, []string) {
	t.Helper()
	var sent []string
	turn, err := h.svc.TakeTurn(context.Background(), caller, r, func(text string) {
		if !slices.ContainsFunc(h.turns.All(), func(saved conversation.Turn) bool { return saved.RequestID == r.RequestID }) {
			t.Errorf("%q sent before the turn was saved", text)
		}
		sent = append(sent, text)
	})
	if err != nil {
		t.Fatalf("TakeTurn: %v", err)
	}
	return turn, sent
}

func noText(t *testing.T) func(string) {
	return func(text string) { t.Errorf("sent %q, want nothing", text) }
}

func TestATurnStreamsTheReplyAndSavesItExactlyForTheNextTurn(t *testing.T) {
	h := newServiceOverFakes(t)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Reads: []string{"session_summary"}, Persona: "You read every log."})
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	first := turnIn(s.ID, "Where is the ferry?")
	first.ScreenInput = []conversation.ScreenInputBlock{{Name: "chart", Content: "Rocks to the north."}}
	firstMessage := "<chart>\nRocks to the north.\n</chart>\n\nWhere is the ferry?"

	turn, sent := h.takeTurn(t, first)
	if !slices.Equal(sent, replyPieces) {
		t.Errorf("sent %q, want the model's pieces in order: %q", sent, replyPieces)
	}
	want := conversation.Turn{
		ID: turn.ID, Workspace: workspace, User: user, Session: s.ID, RequestID: first.RequestID, Seq: 1,
		ScreenInput: first.ScreenInput, Words: first.Words, Reply: reply, Status: conversation.TurnAnswered,
	}
	returned := turn
	returned.Stages = nil // the stage times are checked on their own
	if saved := h.turns.All(); len(saved) != 1 || !reflect.DeepEqual(saved[0], want) || !reflect.DeepEqual(returned, want) {
		t.Errorf("saved %+v, returned %+v; want %+v", saved, turn, want)
	}

	h.takeTurn(t, turnIn(s.ID, "Thanks."))
	asked, _ := h.model.Requests()
	wantFirst := conversation.ModelRequest{Model: "claude-test", Session: s.ID, Prompt: conversation.Prompt{
		StableLayers: []string{"You read every log.", "Learned about the user in earlier conversations:\n\n<session_summary>\nDeckhand, two seasons.\n</session_summary>"},
		Conversation: []conversation.Message{}, NewWords: firstMessage,
	}}
	wantConversation := []conversation.Message{{Role: conversation.RoleUser, Text: firstMessage}, {Role: conversation.RoleAgent, Text: reply}}
	if len(asked) != 2 || !reflect.DeepEqual(asked[0], wantFirst) || !reflect.DeepEqual(asked[1].Conversation, wantConversation) {
		t.Errorf("model asked %+v; want first %+v, then the conversation %q", asked, wantFirst, wantConversation)
	}
}

func TestATurnInASessionThatLoadedNothingDoesntAskUserContext(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, captainID, uuid.NewV7())
	if h.takeTurn(t, turnIn(s.ID, "Is the lamp lit?")); h.items.byID != 0 {
		t.Errorf("user context read %d times, want none", h.items.byID)
	}
}

func TestSessionsOutOfTheCallersReachAreRefusedAlike(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	tests := map[string]struct {
		caller  access.Caller
		session uuid.UUID
	}{
		"a session that doesn't exist": {caller, uuid.NewV7()},
		"another workspace's caller":   {access.Caller{Workspace: uuid.NewV7(), User: user}, s.ID},
		"another user":                 {access.Caller{Workspace: workspace, User: uuid.NewV7()}, s.ID},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := h.svc.TakeTurn(context.Background(), tt.caller, turnIn(tt.session, "Is the lamp lit?"), noText(t)); !errors.Is(err, conversation.ErrNoSuchSession) {
				t.Errorf("TakeTurn = %v, want ErrNoSuchSession", err)
			}
			if _, err := h.svc.Session(context.Background(), tt.caller, tt.session); !errors.Is(err, conversation.ErrNoSuchSession) {
				t.Errorf("Session = %v, want ErrNoSuchSession", err)
			}
		})
	}
	if got, err := h.svc.Session(context.Background(), caller, s.ID); err != nil || got.ID != s.ID {
		t.Errorf("Session(own) = %v, %v; want the session", got.ID, err)
	}
	if asked, _ := h.model.Requests(); len(h.turns.All())+len(asked) != 0 {
		t.Errorf("saved %+v and asked the model %+v; want nothing", h.turns.All(), asked)
	}
}

// The second turn's reply is asked for while its words are being saved, so
// the refused save must stop it.
func TestATurnSentWhileAnotherIsInFlightIsRefusedAndItsReplyStopped(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	var second error
	_, err := h.svc.TakeTurn(context.Background(), caller, turnIn(s.ID, "Is the lamp lit?"), func(text string) {
		if text == replyPieces[0] { // the first turn is in flight while its text is sent
			_, second = h.svc.TakeTurn(context.Background(), caller, turnIn(s.ID, "And the fog?"), noText(t))
		}
	})
	if err != nil || !errors.Is(second, conversation.ErrTurnInFlight) {
		t.Errorf("first turn = %v, second = %v; want the first answered and the second refused", err, second)
	}
	asked, stopped := h.model.Requests()
	if saved := h.turns.All(); len(saved) != 1 || saved[0].Status != conversation.TurnAnswered || len(asked) != 2 || stopped != 1 {
		t.Errorf("saved %+v, model asked %d times and stopped %d; want the first turn answered, the second's reply asked for, then stopped", saved, len(asked), stopped)
	}
}

func TestAReplyTheModelDoesntFinishIsSavedLostWithItsKind(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	h.model.Fail = "overloaded"

	turn, err := h.svc.TakeTurn(context.Background(), caller, turnIn(s.ID, "Is the lamp lit?"), func(string) {})
	var failed *conversation.ReplyFailedError
	if saved := h.turns.All(); !errors.As(err, &failed) || failed.Kind != "overloaded" || turn.Status != conversation.TurnLost || saved[0].Status != conversation.TurnLost {
		t.Errorf("TakeTurn = %+v, %v; saved %+v; want the turn saved lost, failed as overloaded", turn, err, saved)
	}
}

func TestScreenInputThatCantBeUsedIsRefusedBeforeAnythingIsSavedOrSent(t *testing.T) {
	h := newServiceWithLimit(t, 20)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	tests := map[string]struct {
		blocks []conversation.ScreenInputBlock
		want   error
	}{
		"a name a tag can't hold": {[]conversation.ScreenInputBlock{{Name: "tide table>", Content: "Six."}}, &conversation.ScreenInputError{BadNames: []string{"tide table>"}}},
		"no name":                 {[]conversation.ScreenInputBlock{{Content: "Six."}}, &conversation.ScreenInputError{BadNames: []string{""}}},
		"a name sent twice":       {[]conversation.ScreenInputBlock{{Name: "chart", Content: "North."}, {Name: "chart", Content: "South."}}, &conversation.ScreenInputError{Repeated: []string{"chart"}}},
		"nothing in a block":      {[]conversation.ScreenInputBlock{{Name: "chart"}}, &conversation.ScreenInputError{Empty: []string{"chart"}}},
		"a NUL character":         {[]conversation.ScreenInputBlock{{Name: "chart", Content: "North\x00"}}, &conversation.ScreenInputError{Unstorable: []string{"chart"}}},
		"over the limit":          {[]conversation.ScreenInputBlock{{Name: "chart", Content: strings.Repeat("海", 21)}}, conversation.ErrScreenInputTooLarge},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := h.svc.TakeTurn(context.Background(), caller, conversation.TurnRequest{Session: s.ID, RequestID: uuid.NewV7(), Words: "Where?", ScreenInput: tt.blocks}, noText(t))
			var named *conversation.ScreenInputError
			if matches := errors.Is(err, tt.want) || errors.As(err, &named) && reflect.DeepEqual(error(named), tt.want); !matches {
				t.Errorf("TakeTurn = %v, want %v", err, tt.want)
			}
		})
	}
	if asked, _ := h.model.Requests(); len(h.turns.All())+len(asked) != 0 {
		t.Errorf("saved %+v and asked the model %+v; want nothing", h.turns.All(), asked)
	}
	h.takeTurn(t, conversation.TurnRequest{Session: s.ID, RequestID: uuid.NewV7(), Words: "Where?", ScreenInput: []conversation.ScreenInputBlock{{Name: "chart", Content: strings.Repeat("海", 20)}}})
}

func TestWordsATurnCantTakeAreRefusedBeforeAnythingIsSavedOrSent(t *testing.T) {
	h := newServiceWithLimit(t, 20)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	tests := map[string]struct {
		r    conversation.TurnRequest
		want error
	}{
		"no words and no screen input": {turnIn(s.ID, ""), conversation.ErrWordsUnusable},
		"a NUL character":              {turnIn(s.ID, "Where\x00?"), conversation.ErrWordsUnusable},
		"over the limit":               {turnIn(s.ID, strings.Repeat("海", 21)), conversation.ErrWordsTooLong},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := h.svc.TakeTurn(context.Background(), caller, tt.r, noText(t)); !errors.Is(err, tt.want) {
				t.Errorf("TakeTurn = %v, want %v", err, tt.want)
			}
		})
	}
	if asked, _ := h.model.Requests(); len(h.turns.All())+len(asked) != 0 {
		t.Errorf("saved %+v and asked the model %+v; want nothing", h.turns.All(), asked)
	}
	h.takeTurn(t, turnIn(s.ID, strings.Repeat("海", 20)))
	h.takeTurn(t, conversation.TurnRequest{Session: s.ID, RequestID: uuid.NewV7(), ScreenInput: []conversation.ScreenInputBlock{{Name: "chart", Content: "Rocks."}}})
}

func TestARequestIDTheSessionAlreadyTookATurnWithIsRefusedAsARepeat(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	first := turnIn(s.ID, "Is the lamp lit?")
	h.takeTurn(t, first)
	if _, err := h.svc.TakeTurn(context.Background(), caller, first, noText(t)); !errors.Is(err, conversation.ErrTurnRepeated) || len(h.turns.All()) != 1 {
		t.Errorf("TakeTurn again = %v with %d turns saved; want ErrTurnRepeated and one turn", err, len(h.turns.All()))
	}
	if asked, _ := h.model.Requests(); len(asked) != 1 {
		t.Errorf("the model was asked %d times, want once: a repeat of a finished turn costs no model call", len(asked))
	}
}

// beforeReply is a model that runs first before each reply, the moment
// between a turn's prompt being built and its words being saved.
type beforeReply struct {
	conversation.Model
	first func()
}

func (m beforeReply) Reply(ctx context.Context, r conversation.ModelRequest) <-chan conversation.ModelEvent {
	m.first()
	return m.Model.Reply(ctx, r)
}

func TestATurnWhosePromptLacksATurnThatFinishedMeanwhileIsRefused(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	ctx := context.Background()
	inFlight := conversation.Turn{ID: uuid.NewV7(), Workspace: workspace, User: user, Session: s.ID, RequestID: uuid.NewV7(), Words: "Is the lamp lit?"}
	if _, err := h.turns.Start(ctx, inFlight, 0, time.Minute); err != nil {
		t.Fatalf("Start: %v", err)
	}
	svc := h.instance(beforeReply{Model: h.model, first: func() {
		inFlight.Status, inFlight.Reply = conversation.TurnAnswered, "It is."
		if err := h.turns.Finish(ctx, inFlight); err != nil {
			t.Errorf("Finish: %v", err)
		}
	}})
	if _, err := svc.TakeTurn(ctx, caller, turnIn(s.ID, "And the fog?"), noText(t)); !errors.Is(err, conversation.ErrTurnInFlight) || len(h.turns.All()) != 1 || h.turns.All()[0].Status != conversation.TurnAnswered {
		t.Errorf("TakeTurn = %v; saved %+v; want ErrTurnInFlight and only the first turn, answered meanwhile", err, h.turns.All())
	}
}

// finishesAfterRead is the fake store of turns, running then just after the
// session's turns are read for a prompt: a turn finishing meanwhile.
type finishesAfterRead struct {
	*sessionsfake.Turns
	then func()
}

func (f finishesAfterRead) Finished(ctx context.Context, workspace, session uuid.UUID) ([]conversation.Turn, error) {
	saved, err := f.Turns.Finished(ctx, workspace, session)
	f.then()
	return saved, err
}

// A turn cut in on before the model was asked never asks it, so the moment
// between its prompt and its save is right after the prompt reads the turns.
func TestATurnCutInOnBeforeTheModelWhosePromptLacksATurnThatFinishedMeanwhileIsRefused(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	ctx := context.Background()
	inFlight := conversation.Turn{ID: uuid.NewV7(), Workspace: workspace, User: user, Session: s.ID, RequestID: uuid.NewV7(), Words: "Is the lamp lit?"}
	if _, err := h.turns.Start(ctx, inFlight, 0, time.Minute); err != nil {
		t.Fatalf("Start: %v", err)
	}
	svc := conversation.NewService(agents.NewCatalog(h.agents), h.sessions, finishesAfterRead{h.turns, func() {
		inFlight.Status, inFlight.Reply = conversation.TurnAnswered, "It is."
		if err := h.turns.Finish(ctx, inFlight); err != nil {
			t.Errorf("Finish: %v", err)
		}
	}}, h.model, usercontext.NewService(h.items), h.settings)
	cutBefore := make(chan struct{})
	close(cutBefore)
	r := turnIn(s.ID, "And the fog?")
	r.Interrupt = cutBefore

	if _, err := svc.TakeTurn(ctx, caller, r, noText(t)); !errors.Is(err, conversation.ErrTurnInFlight) || len(h.turns.All()) != 1 || h.turns.All()[0].Status != conversation.TurnAnswered {
		t.Errorf("TakeTurn = %v; saved %+v; want ErrTurnInFlight and only the first turn, answered meanwhile", err, h.turns.All())
	}
}

func TestACallerThatGoesAwayMidReplyLeavesTheTurnSavedLostWithTheTextSent(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	ctx, leave := context.WithCancel(context.Background())
	var sent strings.Builder
	_, err := h.svc.TakeTurn(ctx, caller, turnIn(s.ID, "Where is the ferry?"), func(text string) {
		sent.WriteString(text)
		leave()
	})
	var failed *conversation.ReplyFailedError
	if saved := h.turns.All(); !errors.As(err, &failed) || len(saved) != 1 || saved[0].Status != conversation.TurnLost || saved[0].Reply != sent.String() {
		t.Errorf("TakeTurn = %v; saved %+v; want the turn saved lost with the text sent, %q", err, saved, sent.String())
	}
	h.takeTurn(t, turnIn(s.ID, "Are you there?"))
}

// deadlineModel answers at once, recording the deadline its reply had.
type deadlineModel struct{ deadline time.Time }

func (m *deadlineModel) Reply(ctx context.Context, _ conversation.ModelRequest) <-chan conversation.ModelEvent {
	m.deadline, _ = ctx.Deadline()
	events := make(chan conversation.ModelEvent, 1)
	events <- conversation.ModelEvent{Done: true}
	close(events)
	return events
}

// Past the deadline the session's next turn may start, so the turn must be
// over by then, whatever deadline its caller gave.
func TestATurnEndsWithinTheTurnDeadline(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	model, began := &deadlineModel{}, time.Now()
	if _, err := h.instance(model).TakeTurn(context.Background(), caller, turnIn(s.ID, "Is the lamp lit?"), func(string) {}); err != nil {
		t.Fatalf("TakeTurn: %v", err)
	}
	if model.deadline.IsZero() || model.deadline.After(began.Add(h.settings.TurnDeadline)) {
		t.Errorf("the reply's deadline was %v; want one within %v of %v", model.deadline, h.settings.TurnDeadline, began)
	}
}

// runsToItsDeadline is a model that holds its reply until the turn's deadline
// stops it, recording that deadline.
type runsToItsDeadline struct{ deadline *time.Time }

func (m runsToItsDeadline) Reply(ctx context.Context, _ conversation.ModelRequest) <-chan conversation.ModelEvent {
	*m.deadline, _ = ctx.Deadline()
	events := make(chan conversation.ModelEvent)
	go func() {
		defer close(events)
		<-ctx.Done()
	}()
	return events
}

// watchedSaves is the fake store of turns, running afterStart, if set, once
// the words are saved, and recording when the save of how the turn ended
// began and the deadline it was given.
type watchedSaves struct {
	*sessionsfake.Turns
	afterStart func()
	save       *saveTimes
}

type saveTimes struct{ began, by time.Time }

func (w watchedSaves) Start(ctx context.Context, t conversation.Turn, after int, deadline time.Duration) (int, error) {
	seq, err := w.Turns.Start(ctx, t, after, deadline)
	if w.afterStart != nil {
		w.afterStart()
	}
	return seq, err
}

func (w watchedSaves) Finish(ctx context.Context, t conversation.Turn) error {
	w.save.began = time.Now()
	w.save.by, _ = ctx.Deadline()
	return w.Turns.Finish(ctx, t)
}

// The reply's deadline is the turn's, less the time kept for saving; the save
// then gets only that time, so a reply that runs to its deadline is still
// saved by the turn's, after which the session's next turn may start.
func TestASaveAfterAReplyThatRanToItsDeadlineEndsWithinTheTurnDeadline(t *testing.T) {
	h := newServiceOverFakes(t)
	h.settings.TurnDeadline = 200 * time.Millisecond // short, since the reply runs to its deadline
	const kept = 100 * time.Millisecond              // half of a deadline that short
	var replyBy time.Time
	var save saveTimes
	svc := conversation.NewService(agents.NewCatalog(h.agents), h.sessions, watchedSaves{Turns: h.turns, save: &save}, runsToItsDeadline{&replyBy}, usercontext.NewService(h.items), h.settings)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())

	if _, err := svc.TakeTurn(context.Background(), caller, turnIn(s.ID, "Is the lamp lit?"), func(string) {}); err == nil {
		t.Fatal("TakeTurn = nil, want the reply that ran to its deadline lost")
	}
	if save.by.IsZero() || save.by.After(replyBy.Add(kept)) {
		t.Errorf("the save's deadline was %v after the reply's; want at most %v, the turn's own", save.by.Sub(replyBy), kept)
	}
}

// A turn cancelled early, by an interrupt or by voice stopping, waits only
// for its save, so the save gets the time kept for it and no more, even with
// most of the turn deadline left.
func TestASaveAfterATurnThatEndedEarlyGetsOnlyTheTimeKeptForIt(t *testing.T) {
	const kept = 5 * time.Second // the time kept for saving under a minute's turn deadline
	cutBefore := make(chan struct{})
	close(cutBefore)
	for name, interrupt := range map[string]<-chan struct{}{"an answered turn": nil, "a turn cut in on before the model was asked": cutBefore} {
		t.Run(name, func(t *testing.T) {
			h := newServiceOverFakes(t)
			var save saveTimes
			svc := conversation.NewService(agents.NewCatalog(h.agents), h.sessions, watchedSaves{Turns: h.turns, save: &save}, h.model, usercontext.NewService(h.items), h.settings)
			s, _ := h.start(t, caller, keeperID, uuid.NewV7())
			r := turnIn(s.ID, "Is the lamp lit?")
			r.Interrupt = interrupt

			if _, err := svc.TakeTurn(context.Background(), caller, r, func(string) {}); err != nil {
				t.Fatalf("TakeTurn: %v", err)
			}
			if save.by.IsZero() || save.by.After(save.began.Add(kept)) {
				t.Errorf("the save's deadline was %v after it began; want at most %v", save.by.Sub(save.began), kept)
			}
		})
	}
}

// The user's words are saved whenever the interrupt comes, so a caller gone
// once they are saved still leaves the turn ended, within the time kept.
func TestATurnCutInOnBeforeTheModelIsEndedEvenOnceTheCallerHasGone(t *testing.T) {
	const kept = 5 * time.Second // the time kept for saving under a minute's turn deadline
	h := newServiceOverFakes(t)
	ctx, leave := context.WithCancel(context.Background())
	var save saveTimes
	svc := conversation.NewService(agents.NewCatalog(h.agents), h.sessions, watchedSaves{Turns: h.turns, afterStart: leave, save: &save}, h.model, usercontext.NewService(h.items), h.settings)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	cutBefore := make(chan struct{})
	close(cutBefore)
	r := turnIn(s.ID, "Where is the ferry?")
	r.Interrupt = cutBefore

	_, err := svc.TakeTurn(ctx, caller, r, noText(t))
	if saved := h.turns.All(); err != nil || len(saved) != 1 || saved[0].Status != conversation.TurnAnswered || saved[0].Words != r.Words {
		t.Errorf("TakeTurn = %v; saved %+v; want the words saved and the turn ended", err, saved)
	}
	if save.by.IsZero() || save.by.After(save.began.Add(kept)) {
		t.Errorf("the save's deadline was %v after it began; want at most %v", save.by.Sub(save.began), kept)
	}
}

// No instance holds a session's conversation, so any instance can take its
// next turn.
func TestATurnServedByAFreshInstanceContinuesTheConversation(t *testing.T) {
	h := newServiceOverFakes(t)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Reads: []string{"session_summary"}, Persona: "You read every log."})
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	h.takeTurn(t, turnIn(s.ID, "Where is the ferry?"))
	if _, err := h.instance(h.model).TakeTurn(context.Background(), caller, turnIn(s.ID, "Thanks."), func(string) {}); err != nil {
		t.Fatalf("TakeTurn on a fresh instance: %v", err)
	}
	asked, _ := h.model.Requests()
	want := []conversation.Message{{Role: conversation.RoleUser, Text: "Where is the ferry?"}, {Role: conversation.RoleAgent, Text: reply}}
	if len(asked) != 2 || !reflect.DeepEqual(asked[1].Conversation, want) || !slices.Equal(asked[1].StableLayers, asked[0].StableLayers) {
		t.Errorf("model asked %+v; want the second turn with the first's stable layers and the conversation %q", asked, want)
	}
}

// interruptAfterFirstPiece returns a turn request whose interrupt comes as the
// first piece of the reply is sent, and the send that records the pieces sent.
func interruptAfterFirstPiece(r conversation.TurnRequest) (conversation.TurnRequest, func(string), *[]string) {
	interrupt := make(chan struct{})
	r.Interrupt = interrupt
	var once sync.Once
	var sent []string
	return r, func(text string) {
		sent = append(sent, text)
		once.Do(func() { close(interrupt) })
	}, &sent
}

// The interrupt closes while the model's next piece is ready too, where a
// select alone picks either at random; so the turn is taken many times over,
// and a check left out fails all but by chance.
func TestAnInterruptStopsTheReplyAndSavesTheTurnAnsweredWithTheTextSoFar(t *testing.T) {
	h := newServiceOverFakes(t)
	for range 30 {
		s, _ := h.start(t, caller, keeperID, uuid.NewV7())
		r, send, sent := interruptAfterFirstPiece(turnIn(s.ID, "Where is the ferry?"))

		turn, err := h.svc.TakeTurn(context.Background(), caller, r, send)
		saved := h.turns.All()
		last := saved[len(saved)-1]
		if err != nil || last.Status != conversation.TurnAnswered || last.Reply != replyPieces[0] || turn.Reply != replyPieces[0] {
			t.Fatalf("TakeTurn = %+v, %v; saved %+v; want the turn answered with the first piece", turn, err, last)
		}
		if !slices.Equal(*sent, replyPieces[:1]) {
			t.Fatalf("sent %q, want nothing after the interrupt: %q", *sent, replyPieces[:1])
		}
	}
	if _, stopped := h.model.Requests(); stopped != 30 {
		t.Errorf("model stopped %d replies, want all 30", stopped)
	}
}

// failsThenInterrupted fails the reply, then has the user cut in before the
// reply is over.
type failsThenInterrupted struct{ interrupt chan struct{} }

func (m failsThenInterrupted) Reply(ctx context.Context, _ conversation.ModelRequest) <-chan conversation.ModelEvent {
	events := make(chan conversation.ModelEvent)
	go func() {
		defer close(events)
		events <- conversation.ModelEvent{Error: "overloaded"}
		close(m.interrupt)
		<-ctx.Done()
	}()
	return events
}

func TestAnInterruptAfterTheModelFailedKeepsTheFailure(t *testing.T) {
	h := newServiceOverFakes(t)
	model := failsThenInterrupted{interrupt: make(chan struct{})}
	h.svc = h.instance(model)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	r := turnIn(s.ID, "Where is the ferry?")
	r.Interrupt = model.interrupt

	turn, err := h.svc.TakeTurn(context.Background(), caller, r, noText(t))
	var failed *conversation.ReplyFailedError
	if !errors.As(err, &failed) || failed.Kind != "overloaded" || turn.Status != conversation.TurnLost {
		t.Errorf("TakeTurn = %+v, %v; want it lost as overloaded", turn, err)
	}
	if got, err := h.svc.Interrupted(context.Background(), turn, 0); err != nil || got.Status != conversation.TurnLost || h.turns.All()[0].Status != conversation.TurnLost {
		t.Errorf("Interrupted = %+v, %v; want a lost turn left lost", got, err)
	}
}

func TestAnInterruptBeforeTheReplyStartsStillSavesTheWords(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	interrupt := make(chan struct{})
	close(interrupt)
	r := turnIn(s.ID, "Where is the ferry?")
	r.Interrupt = interrupt

	turn, err := h.svc.TakeTurn(context.Background(), caller, r, noText(t))
	if saved := h.turns.All(); err != nil || len(saved) != 1 || saved[0].Words != r.Words || saved[0].Status != conversation.TurnAnswered || turn.Reply != "" {
		t.Errorf("TakeTurn = %+v, %v; saved %+v; want the words saved, answered with no reply", turn, err, saved)
	}
	// Cut, so the model learns the user cut in rather than that it said nothing.
	if got, err := h.svc.Interrupted(context.Background(), turn, 0); err != nil || got.Status != conversation.TurnCut || h.turns.All()[0].Status != conversation.TurnCut {
		t.Errorf("Interrupted = %+v, %v; want a turn with no reply cut with nothing heard", got, err)
	}
}

func TestAReplyIsSavedAsFarAsItWasHeardAndTheNextTurnSeesWhereItWasCut(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	turn, _ := h.takeTurn(t, turnIn(s.ID, "Where is the ferry?"))
	heard := len(replyPieces[0])

	cut, err := h.svc.Interrupted(context.Background(), turn, heard)
	want := turn
	want.Status, want.Heard = conversation.TurnCut, heard
	if saved := h.turns.All(); err != nil || !reflect.DeepEqual(cut, want) || saved[0].Status != conversation.TurnCut || saved[0].Heard != heard || saved[0].Reply != reply {
		t.Errorf("Interrupted = %+v, %v; saved %+v; want it cut after %d bytes, the whole reply kept", cut, err, saved, heard)
	}

	h.takeTurn(t, turnIn(s.ID, "Which ferry?"))
	asked, _ := h.model.Requests()
	wantConversation := []conversation.Message{{Role: conversation.RoleUser, Text: "Where is the ferry?"}, {Role: conversation.RoleAgent, Text: "  **Bearing—"}}
	wantWords := "(The user interrupted you; your last reply ends where they stopped hearing it.)\n\nWhich ferry?"
	if !reflect.DeepEqual(asked[1].Conversation, wantConversation) || asked[1].NewWords != wantWords {
		t.Errorf("next turn asked with %q and new words %q; want %q and %q", asked[1].Conversation, asked[1].NewWords, wantConversation, wantWords)
	}
}

// Text-to-speech never speaks a reply's trailing space, so the end of its
// last phrase is the end of what can be heard.
func TestAReplyHeardToItsLastWordIsLeftAsItWas(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	turn, _ := h.takeTurn(t, turnIn(s.ID, "Where is the ferry?"))

	got, err := h.svc.Interrupted(context.Background(), turn, len(strings.TrimRight(reply, "\n")))
	if saved := h.turns.All(); err != nil || got.Status != conversation.TurnAnswered || saved[0].Status != conversation.TurnAnswered {
		t.Errorf("Interrupted = %+v, %v; saved %+v; want the turn left answered", got, err, saved)
	}
}

func TestATurnInterruptedBeforeAnythingWasHeardIsCutWithNothingHeard(t *testing.T) {
	for name, heard := range map[string]int{"nothing heard": 0, "a point before the reply": -3} {
		t.Run(name, func(t *testing.T) {
			h := newServiceOverFakes(t)
			s, _ := h.start(t, caller, keeperID, uuid.NewV7())
			turn, _ := h.takeTurn(t, turnIn(s.ID, "Where is the ferry?"))

			got, err := h.svc.Interrupted(context.Background(), turn, heard)
			if err != nil || got.Status != conversation.TurnCut || got.Heard != 0 || h.turns.All()[0].Heard != 0 {
				t.Errorf("Interrupted = %+v, %v; want it cut with nothing heard", got, err)
			}
		})
	}
}

func TestAHeardPointInsideACharacterMovesBackToTheCharactersStart(t *testing.T) {
	h := newServiceOverFakes(t)
	h.model.Pieces = []string{"Café olé"}
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	turn, _ := h.takeTurn(t, turnIn(s.ID, "Where to?"))

	got, err := h.svc.Interrupted(context.Background(), turn, strings.Index("Café olé", "é")+1)
	if err != nil || got.Heard != len("Caf") || h.turns.All()[0].Heard != len("Caf") {
		t.Errorf("Interrupted = %+v, %v; want it heard up to %q", got, err, "Caf")
	}
}

func TestAnInterruptIsSavedEvenOnceTheCallerHasGone(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	turn, _ := h.takeTurn(t, turnIn(s.ID, "Where is the ferry?"))
	gone, leave := context.WithCancel(context.Background())
	leave()

	if _, err := h.svc.Interrupted(gone, turn, 2); err != nil || h.turns.All()[0].Status != conversation.TurnCut {
		t.Errorf("Interrupted = %v; saved %+v; want it cut", err, h.turns.All())
	}
}

func TestATurnALaterTurnFollowedIsNotCut(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	first, _ := h.takeTurn(t, turnIn(s.ID, "Where is the ferry?"))
	h.takeTurn(t, turnIn(s.ID, "Thanks."))

	if _, err := h.svc.Interrupted(context.Background(), first, 2); err == nil || h.turns.All()[0].Status != conversation.TurnAnswered {
		t.Errorf("Interrupted = %v; saved %+v; want it refused and the turn left answered", err, h.turns.All())
	}
}

func TestATurnCutInOnBeforeTheModelIsAskedNeverAsksIt(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	interrupt := make(chan struct{})
	close(interrupt)
	r := turnIn(s.ID, "Where is the ferry?")
	r.Interrupt = interrupt

	turn, err := h.svc.TakeTurn(context.Background(), caller, r, noText(t))
	var stages []string
	for _, st := range turn.Stages {
		stages = append(stages, st.Name)
	}
	if asked, _ := h.model.Requests(); err != nil || len(asked) != 0 || !turn.Unfinished || h.turns.All()[0].Words != r.Words {
		t.Errorf("TakeTurn = %+v, %v; model asked %d times; want the words saved, the turn unfinished, the model never asked", turn, err, len(asked))
	}
	if want := []string{conversation.StagePrompt, conversation.StageSave}; !slices.Equal(stages, want) {
		t.Errorf("stages %q, want only %q: the model wasn't asked", stages, want)
	}
}

func TestAReplyCutInOnBeforeTheModelFinishedButHeardToItsLastWordIsStillCut(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	r, send, _ := interruptAfterFirstPiece(turnIn(s.ID, "Where is the ferry?"))
	turn, err := h.svc.TakeTurn(context.Background(), caller, r, send)
	if err != nil || !turn.Unfinished {
		t.Fatalf("TakeTurn = %+v, %v; want the turn unfinished: the interrupt came first", turn, err)
	}

	cut, err := h.svc.Interrupted(context.Background(), turn, len(replyPieces[0]))
	if err != nil || cut.Status != conversation.TurnCut || cut.Heard != len(turn.Reply) {
		t.Errorf("Interrupted = %+v, %v; want it cut with all %d bytes heard: the model hadn't finished", cut, err, len(turn.Reply))
	}
	h.takeTurn(t, turnIn(s.ID, "Which ferry?"))
	asked, _ := h.model.Requests()
	if got := asked[1].Conversation[1].Text; got != "  **Bearing—" {
		t.Errorf("the next turn showed the reply as %q, want it cut off with a dash", got)
	}
}

// doneAsInterrupted sends one piece, then, once the interrupt has closed, puts
// its final Done where the turn will find it, as a model that finished just
// as the user cut in does.
type doneAsInterrupted struct {
	interrupt <-chan struct{}
	queued    chan struct{}
}

func (m *doneAsInterrupted) Reply(ctx context.Context, _ conversation.ModelRequest) <-chan conversation.ModelEvent {
	events := make(chan conversation.ModelEvent, 1)
	go func() {
		defer close(events)
		events <- conversation.ModelEvent{Text: "It is."}
		select {
		case <-m.interrupt:
		case <-ctx.Done():
			return
		}
		events <- conversation.ModelEvent{Done: true, Report: &conversation.ModelReport{Provider: "test", Model: "finished"}}
		close(m.queued)
	}()
	return events
}

func TestAReplyTheModelFinishedAsTheUserCutInIsNotUnfinished(t *testing.T) {
	h := newServiceOverFakes(t)
	model := &doneAsInterrupted{}
	svc := h.instance(model)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	// Both the interrupt and the Done are ready when the turn next looks, and
	// it may see either first: a hundred tries make a miss all but certain.
	for range 100 {
		interrupt := make(chan struct{})
		model.interrupt, model.queued = interrupt, make(chan struct{})
		r := turnIn(s.ID, "Is the lamp lit?")
		r.Interrupt = interrupt
		turn, err := svc.TakeTurn(context.Background(), caller, r, func(string) {
			close(interrupt)
			<-model.queued
		})
		if err != nil || turn.Unfinished || turn.Model() != "test/finished" {
			t.Fatalf("TakeTurn = %+v, %v; want it finished, with the model's report", turn, err)
		}
	}
}

// endsAsInterrupted sends one piece, then, once the interrupt has closed, lets
// go of the reply with no final event, as a model stopped just as the user cut
// in does.
type endsAsInterrupted struct {
	interrupt <-chan struct{}
	ended     chan struct{}
}

func (m *endsAsInterrupted) Reply(ctx context.Context, _ conversation.ModelRequest) <-chan conversation.ModelEvent {
	events := make(chan conversation.ModelEvent, 1)
	interrupt, ended := m.interrupt, m.ended
	go func() {
		defer close(ended)
		defer close(events)
		events <- conversation.ModelEvent{Text: "It is."}
		select {
		case <-interrupt:
		case <-ctx.Done():
		}
	}()
	return events
}

func TestAReplyTheModelLetGoOfAsTheUserCutInIsUnfinishedNotLost(t *testing.T) {
	h := newServiceOverFakes(t)
	model := &endsAsInterrupted{}
	svc := h.instance(model)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	// Both the interrupt and the reply's end are ready when the turn next
	// looks, and it may see either first: a hundred tries make a miss all but
	// certain.
	for range 100 {
		interrupt := make(chan struct{})
		model.interrupt, model.ended = interrupt, make(chan struct{})
		r := turnIn(s.ID, "Is the lamp lit?")
		r.Interrupt = interrupt
		turn, err := svc.TakeTurn(context.Background(), caller, r, func(string) {
			close(interrupt)
			<-model.ended
		})
		if err != nil || !turn.Unfinished || turn.Status != conversation.TurnAnswered {
			t.Fatalf("TakeTurn = %+v, %v; want it answered and unfinished, not lost", turn, err)
		}
	}
}

// writesOn sends a piece, then writes on until its reply is stopped, and
// sends one more piece as it stops, as a model a little slow to stop does. It
// lets go of the reply only once that piece is taken.
type writesOn struct {
	ctx   context.Context // the reply's, to see whether it was stopped
	letGo chan struct{}   // closed as the model lets go of the reply
}

func (m *writesOn) Reply(ctx context.Context, _ conversation.ModelRequest) <-chan conversation.ModelEvent {
	m.ctx, m.letGo = ctx, make(chan struct{})
	events, letGo := make(chan conversation.ModelEvent), m.letGo
	go func() {
		defer close(events)
		defer close(letGo)
		select {
		case events <- conversation.ModelEvent{Text: "It is."}:
		case <-ctx.Done():
			return
		}
		<-ctx.Done()
		events <- conversation.ModelEvent{Text: " Lit."}
	}()
	return events
}

// stopSeenAtSave is the fake store of turns, noting whether the model's reply
// had been stopped by the time how the turn ended was saved.
type stopSeenAtSave struct {
	*sessionsfake.Turns
	model   *writesOn
	stopped *bool
}

func (s stopSeenAtSave) Finish(ctx context.Context, t conversation.Turn) error {
	*s.stopped = s.model.ctx.Err() != nil
	return s.Turns.Finish(ctx, t)
}

// A model stopped only after the save would write on meanwhile, for nobody.
func TestACutInStopsTheModelBeforeTheTurnIsSaved(t *testing.T) {
	h := newServiceOverFakes(t)
	model := &writesOn{}
	var stopped bool
	svc := conversation.NewService(agents.NewCatalog(h.agents), h.sessions, stopSeenAtSave{h.turns, model, &stopped}, model, usercontext.NewService(h.items), h.settings)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	r, send, _ := interruptAfterFirstPiece(turnIn(s.ID, "Is the lamp lit?"))

	if turn, err := svc.TakeTurn(context.Background(), caller, r, send); err != nil || !turn.Unfinished || !stopped {
		t.Errorf("TakeTurn = %+v, %v; stopped before the save: %v; want the turn unfinished and the model stopped first", turn, err, stopped)
	}
}

// The turn started the model's reply, so it waits for the reply to end
// rather than leave it running, and sends on nothing the model sent after
// the user cut in.
func TestATurnReturnsOnlyOnceTheModelLetsGoOfItsReply(t *testing.T) {
	h := newServiceOverFakes(t)
	model := &writesOn{}
	svc := h.instance(model)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	r, send, sent := interruptAfterFirstPiece(turnIn(s.ID, "Is the lamp lit?"))

	_, err := svc.TakeTurn(context.Background(), caller, r, send)
	select {
	case <-model.letGo:
	default:
		t.Error("TakeTurn returned while the model still held its reply")
	}
	if err != nil || !slices.Equal(*sent, []string{"It is."}) {
		t.Errorf("TakeTurn = %v; sent %q; want only the piece before the interrupt", err, *sent)
	}
}

// failingFinish is the fake store of turns, failing to save how a turn ended.
type failingFinish struct{ *sessionsfake.Turns }

// errDiskFull is the store's failure, which the turn's error keeps for callers to test.
var errDiskFull = errors.New("disk full")

func (failingFinish) Finish(context.Context, conversation.Turn) error { return errDiskFull }

// The turn stays in flight, holding up its session until the deadline, so
// the error says which turn it is and why its reply ended. It is not the
// reply's failure, which callers answer by naming a turn saved as ended.
func TestASaveThatFailsNamesTheTurnAndWhyItsReplyEnded(t *testing.T) {
	cutBefore := make(chan struct{})
	close(cutBefore)
	tests := map[string]struct {
		fail      conversation.ModelErrorKind
		interrupt <-chan struct{}
		why       string
	}{
		"a reply the model finished":                  {"", nil, ""},
		"a reply the model failed":                    {"overloaded", nil, " (the model's reply failed: overloaded)"},
		"a turn cut in on before the model was asked": {"", cutBefore, ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			h := newServiceOverFakes(t)
			h.model.Fail = tt.fail
			svc := conversation.NewService(agents.NewCatalog(h.agents), h.sessions, failingFinish{h.turns}, h.model, usercontext.NewService(h.items), h.settings)
			s, _ := h.start(t, caller, keeperID, uuid.NewV7())
			r := turnIn(s.ID, "Is the lamp lit?")
			r.Interrupt = tt.interrupt

			_, err := svc.TakeTurn(context.Background(), caller, r, func(string) {})
			want := fmt.Sprintf("save how turn %s, number 1, ended%s: disk full", h.turns.All()[0].ID, tt.why)
			if failed := new(conversation.ReplyFailedError); err == nil || err.Error() != want || !errors.Is(err, errDiskFull) || errors.As(err, &failed) {
				t.Errorf("TakeTurn = %v; want %q, not the reply's failure", err, want)
			}
		})
	}
}

func TestATurnAsksForTheModelItsAgentVersionNamesElseTheServers(t *testing.T) {
	for _, tt := range []struct{ named, want string }{{"claude-sonnet-5-5", "claude-sonnet-5-5"}, {"", "claude-test"}} {
		h := newServiceOverFakes(t)
		h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You read every log.", Model: tt.named})
		s, _ := h.start(t, caller, keeperID, uuid.NewV7())
		h.takeTurn(t, turnIn(s.ID, "Where is the ferry?"))
		asked, _ := h.model.Requests()
		if len(asked) != 1 || asked[0].Model != tt.want {
			t.Errorf("with model %q the model was asked %+v, want %q", tt.named, asked, tt.want)
		}
	}
}

func TestASessionStartedBeforeARepublishKeepsItsVersionsModel(t *testing.T) {
	h := newServiceOverFakes(t)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You read every log.", Model: "claude-sonnet-5-5"})
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You read every log.", Model: "claude-haiku-5-5"})
	h.takeTurn(t, turnIn(s.ID, "Where is the ferry?"))
	if asked, _ := h.model.Requests(); len(asked) != 1 || asked[0].Model != "claude-sonnet-5-5" {
		t.Errorf("the model was asked %+v, want claude-sonnet-5-5, the model of the version the session started on", asked)
	}
	later, _ := h.start(t, caller, keeperID, uuid.NewV7())
	h.takeTurn(t, turnIn(later.ID, "Where is the ferry?"))
	if asked, _ := h.model.Requests(); len(asked) != 2 || asked[1].Model != "claude-haiku-5-5" {
		t.Errorf("the model was asked %+v, want claude-haiku-5-5 for a session started after the republish", asked)
	}
}

func TestATurnSendsTheCriteriaOfItsSessionsAgentVersionEvenAfterANewOneIsPublished(t *testing.T) {
	h := newServiceOverFakes(t)
	graded := agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea.", Summary: instruction, Grading: agents.Grading{
		Lowest: 1, Highest: 5, Criteria: []agents.Criterion{{Name: "fog_calls", LooksFor: "Answers every call in the fog."}},
	}}
	h.publish(t, graded)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	h.takeTurn(t, turnIn(s.ID, "Is the lamp lit?"))
	graded.Grading.Criteria[0] = agents.Criterion{Name: "lamp_care", LooksFor: "Keeps the lamp lit."}
	h.publish(t, graded)
	h.takeTurn(t, turnIn(s.ID, "And the fog?"))

	asked, _ := h.model.Requests()
	if len(asked) != 2 {
		t.Fatalf("the model was asked %d times, want 2", len(asked))
	}
	for i, r := range asked {
		if layer := r.StableLayers[0]; !strings.Contains(layer, "<fog_calls>\nAnswers every call in the fog.\n</fog_calls>") || strings.Contains(layer, "lamp_care") {
			t.Errorf("turn %d's agent layer = %q, want the session's version's criteria only", i+1, layer)
		}
	}
	if asked[0].StableLayers[0] != asked[1].StableLayers[0] {
		t.Error("the agent layer changed between the session's turns, want the same bytes")
	}
}
