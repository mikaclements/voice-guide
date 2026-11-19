package conversation_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/conversation"
	sessionsfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/usercontext"
)

func names(stages []conversation.Stage) []string {
	out := make([]string, 0, len(stages))
	for _, s := range stages {
		out = append(out, s.Name)
	}
	return out
}

func TestEveryTurnRecordsEachStageWithWhatServedIt(t *testing.T) {
	h := newServiceOverFakes(t)
	firstText := 610 * time.Millisecond
	report := conversation.ModelReport{
		Provider: "claude-subscription", Model: "claude-sonnet-5-5",
		Usage:     &conversation.ModelUsage{Input: 12, CachedInput: 1340, CacheWrite: 58, Output: 31},
		FirstText: &firstText, Total: 1270 * time.Millisecond,
	}
	h.model.Report = &report
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	turn, _ := h.takeTurn(t, turnIn(s.ID, "Is the lamp lit?"))
	if err := h.svc.RecordTiming(context.Background(), turn); err != nil {
		t.Fatalf("RecordTiming: %v", err)
	}

	got := h.turns.Stages(turn.ID)
	want := []string{conversation.StagePrompt, conversation.StageModelFirstText, conversation.StageModelReply, conversation.StageSave}
	if !slices.Equal(names(got), want) {
		t.Fatalf("stages = %v, want %v", names(got), want)
	}
	for _, st := range got {
		served := ""
		if st.Name == conversation.StageModelFirstText || st.Name == conversation.StageModelReply {
			served = "claude-subscription/claude-sonnet-5-5"
		}
		if st.Implementation != served || st.Started.IsZero() || st.Took < 0 {
			t.Errorf("%s = %+v, want served by %q, with a start and a duration", st.Name, st, served)
		}
	}
	if reply := got[2]; reply.Report == nil || *reply.Report.Usage != *report.Usage || *reply.Report.FirstText != firstText {
		t.Errorf("reply stage report = %+v, want the model service's report with its usage", reply.Report)
	}
}

func TestAReplyThatSentNoTextRecordsNoFirstTextAndTheModelAskedFor(t *testing.T) {
	h := newServiceOverFakes(t)
	h.model.Pieces, h.model.Fail = nil, "overloaded"
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	turn, err := h.svc.TakeTurn(context.Background(), caller, turnIn(s.ID, "Is the lamp lit?"), noText(t))
	if failed := new(conversation.ReplyFailedError); !errors.As(err, &failed) {
		t.Fatalf("TakeTurn = %v, want the reply's failure", err)
	}
	if err := h.svc.RecordTiming(context.Background(), turn); err != nil {
		t.Fatalf("RecordTiming: %v", err)
	}
	got := h.turns.Stages(turn.ID)
	if want := []string{conversation.StagePrompt, conversation.StageModelReply, conversation.StageSave}; !slices.Equal(names(got), want) {
		t.Fatalf("stages = %v, want %v", names(got), want)
	}
	if reply := got[1]; reply.Implementation != "claude-test" || reply.Report != nil {
		t.Errorf("reply stage = %+v, want the model asked for and no report", reply)
	}
}

func TestStageTimesAreSavedEvenOnceTheCallerHasGone(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	turn, _ := h.takeTurn(t, turnIn(s.ID, "Is the lamp lit?"))
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.svc.RecordTiming(gone, turn); err != nil || len(h.turns.Stages(turn.ID)) == 0 {
		t.Errorf("RecordTiming after the caller went = %v with %d stages, want them saved", err, len(h.turns.Stages(turn.ID)))
	}
}

// unreportedModel sends one piece, then done without a report, as the model's
// client does when the model service's report doesn't read.
type unreportedModel struct{}

func (unreportedModel) Reply(context.Context, conversation.ModelRequest) <-chan conversation.ModelEvent {
	events := make(chan conversation.ModelEvent, 2)
	events <- conversation.ModelEvent{Text: "It is."}
	events <- conversation.ModelEvent{Done: true}
	close(events)
	return events
}

func TestAReplyDoneWithoutAReportRecordsTheModelAskedFor(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	turn, err := h.instance(unreportedModel{}).TakeTurn(context.Background(), caller, turnIn(s.ID, "Is the lamp lit?"), func(string) {})
	if err != nil || turn.Model() != "claude-test" {
		t.Fatalf("TakeTurn = model %q, %v; want the turn answered by the model asked for", turn.Model(), err)
	}
	for _, st := range turn.Stages[1:3] {
		if st.Implementation != "claude-test" || st.Report != nil {
			t.Errorf("%s = %+v, want served by the model asked for, with no report", st.Name, st)
		}
	}
}

// steppedClock is the time as a test moves it: only the fakes doing a turn's
// work step it, so each stage's time is exactly the work inside it.
type steppedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *steppedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *steppedClock) step(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// steppedTurns is the fake store of turns, where reading the session's turns
// takes 10ms on the clock, saving the words 20ms and saving how the turn
// ended 40ms.
type steppedTurns struct {
	*sessionsfake.Turns
	clock *steppedClock
}

func (s steppedTurns) Finished(ctx context.Context, workspace, session uuid.UUID) ([]conversation.Turn, error) {
	s.clock.step(10 * time.Millisecond)
	return s.Turns.Finished(ctx, workspace, session)
}

func (s steppedTurns) Start(ctx context.Context, t conversation.Turn, after int, deadline time.Duration) (int, error) {
	s.clock.step(20 * time.Millisecond)
	return s.Turns.Start(ctx, t, after, deadline)
}

func (s steppedTurns) Finish(ctx context.Context, t conversation.Turn) error {
	s.clock.step(40 * time.Millisecond)
	return s.Turns.Finish(ctx, t)
}

// steppedModel takes 100ms on the clock to its one piece and, once the turn
// has sent the piece on, 200ms more to its final event, done unless final
// says otherwise: waiting keeps the second step out of the moment the turn
// reads the clock for its first text.
type steppedModel struct {
	clock *steppedClock
	sent  chan struct{}
	final conversation.ModelEvent
}

func (m steppedModel) Reply(ctx context.Context, _ conversation.ModelRequest) <-chan conversation.ModelEvent {
	events := make(chan conversation.ModelEvent)
	go func() {
		defer close(events)
		m.clock.step(100 * time.Millisecond)
		select {
		case events <- conversation.ModelEvent{Text: "It is."}:
		case <-ctx.Done():
			return
		}
		select {
		case <-m.sent:
		case <-ctx.Done():
			return
		}
		m.clock.step(200 * time.Millisecond)
		final := m.final
		final.Done = final.Error == ""
		select {
		case events <- final:
		case <-ctx.Done():
		}
	}()
	return events
}

func TestEachStageTakesExactlyTheTimeOfTheWorkInsideIt(t *testing.T) {
	type stage struct {
		Name       string
		Start, End time.Duration // since the turn began
	}
	const beforeTheModel, midReply = "before the model", "mid-reply"
	// The words are saved while the model starts, so their 20ms fall inside the model's stages.
	whole := []stage{
		{conversation.StagePrompt, 0, 10 * time.Millisecond},
		{conversation.StageModelFirstText, 10 * time.Millisecond, 130 * time.Millisecond},
		{conversation.StageModelReply, 10 * time.Millisecond, 330 * time.Millisecond},
		{conversation.StageSave, 330 * time.Millisecond, 370 * time.Millisecond},
	}
	tests := map[string]struct {
		cut   string
		final conversation.ModelEvent
		want  []stage
	}{
		"an answered turn": {"", conversation.ModelEvent{}, whole},
		"a lost reply":     {"", conversation.ModelEvent{Error: "overloaded"}, whole},
		// The reply ends where the user cut in, at its first piece.
		"a reply cut mid-stream": {midReply, conversation.ModelEvent{}, []stage{
			{conversation.StagePrompt, 0, 10 * time.Millisecond},
			{conversation.StageModelFirstText, 10 * time.Millisecond, 130 * time.Millisecond},
			{conversation.StageModelReply, 10 * time.Millisecond, 130 * time.Millisecond},
			{conversation.StageSave, 130 * time.Millisecond, 170 * time.Millisecond},
		}},
		// No model runs while the words are saved, so the save stage holds both writes.
		"a turn cut in on before the model was asked": {beforeTheModel, conversation.ModelEvent{}, []stage{
			{conversation.StagePrompt, 0, 10 * time.Millisecond},
			{conversation.StageSave, 10 * time.Millisecond, 70 * time.Millisecond},
		}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			h := newServiceOverFakes(t)
			began := time.Date(2026, 3, 1, 21, 0, 0, 0, time.UTC)
			clock := &steppedClock{now: began}
			model := steppedModel{clock, make(chan struct{}, 1), tt.final}
			svc := conversation.NewService(agents.NewCatalog(h.agents), h.sessions, steppedTurns{h.turns, clock}, model, usercontext.NewService(h.items), h.settings)
			conversation.SetClock(svc, clock.Now)
			s, _ := h.start(t, caller, keeperID, uuid.NewV7())
			r := turnIn(s.ID, "Is the lamp lit?")
			interrupt := make(chan struct{})
			r.Interrupt = interrupt
			if tt.cut == beforeTheModel {
				close(interrupt)
			}
			send := func(string) {
				if tt.cut == midReply {
					close(interrupt)
					return
				}
				model.sent <- struct{}{}
			}

			turn, err := svc.TakeTurn(context.Background(), caller, r, send)
			got := make([]stage, 0, len(turn.Stages))
			for _, st := range turn.Stages {
				got = append(got, stage{st.Name, st.Started.Sub(began), st.Started.Add(st.Took).Sub(began)})
			}
			if lost := tt.final.Error != ""; (err != nil) != lost || !slices.Equal(got, tt.want) {
				t.Errorf("TakeTurn = %v; stages (name, start, end) %v, want %v", err, got, tt.want)
			}
		})
	}
}
