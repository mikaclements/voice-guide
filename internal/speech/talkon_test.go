package speech_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mikaclements/voice-guide/internal/conversation"
	modelfake "github.com/mikaclements/voice-guide/internal/conversation/model/fake"
	"github.com/mikaclements/voice-guide/internal/conversation/store/postgres"
	"github.com/mikaclements/voice-guide/internal/pgtest"
	"github.com/mikaclements/voice-guide/internal/speech"
	detectionfake "github.com/mikaclements/voice-guide/internal/speech/detection/fake"
	"github.com/mikaclements/voice-guide/internal/speech/transport/fake"
	ttsfake "github.com/mikaclements/voice-guide/internal/speech/tts/fake"
)

// judging is an end-of-turn check the test answers: each check asked comes out on asked, and waits
// for its answer or to be stopped.
type judging struct{ asked chan chan bool }

func (j judging) Finished(ctx context.Context, _ []int16) (bool, error) {
	answer := make(chan bool, 1)
	select {
	case j.asked <- answer:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	select {
	case finished := <-answer:
		return finished, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// judge answers the next check asked.
func (j judging) judge(t *testing.T, finished bool) {
	t.Helper()
	select {
	case answer := <-j.asked:
		answer <- finished
	case <-time.After(wait):
		t.Fatal("the end-of-turn check wasn't asked")
	}
}

// Speech for 5 frames, quiet for 10, then speech for 5 again: a pause, then speaking on.
var speakingTwice = detectionfake.Detection{Script: slices.Concat(slices.Repeat([]float32{0.9}, 5), make([]float32, 10), slices.Repeat([]float32{0.9}, 5))}

// both is what speech-to-text hears of speakingTwice.
const both = "Where should we start? Where should we start?"

// newJudgedHarness has the test answer the end-of-turn check, and saves turns through wrap's store
// when wrap is set.
func newJudgedHarness(t *testing.T, d speech.SpeechDetection, tts speech.TextToSpeech, wrap func(conversation.Turns) conversation.Turns) (turnHarness, *modelfake.Model, judging, *fake.Connection) {
	t.Helper()
	model := &modelfake.Model{Pieces: replyPieces}
	check := judging{asked: make(chan chan bool, 4)}
	h := newHearingTurnHarness(t, model, tts, slog.New(slog.DiscardHandler), wrap, d, saying, check)
	return h, model, check, h.connect(t)
}

// heldFirst holds the first reply's speech back until release is closed, as text-to-speech slow
// to make its first phrase does.
type heldFirst struct {
	speech.TextToSpeech
	release chan struct{}
	calls   atomic.Int32
}

func (h *heldFirst) Speak(ctx context.Context, voice string, text <-chan string) <-chan speech.Packet {
	packets := h.TextToSpeech.Speak(ctx, voice, text)
	if h.calls.Add(1) > 1 {
		return packets
	}
	out := make(chan speech.Packet)
	go func() {
		defer close(out)
		select {
		case <-h.release:
		case <-ctx.Done():
		}
		for p := range packets { // ends with ctx, so the speech under it finishes
			select {
			case out <- p:
			case <-ctx.Done():
			}
		}
	}()
	return out
}

// talkOn takes the first utterance of speakingTwice as a turn whose reply hasn't sounded yet, then
// speaks again, and returns the turn's request ID once the page is told the user talked on.
func talkOn(t *testing.T, c *fake.Connection, check judging) string {
	t.Helper()
	speakFrames(c, 12)
	check.judge(t, true)
	request := until(t, c, "text")[0].RequestID // taken, its text shown, its speech not yet made
	speakFrames(c, 4)                           // speaking again before any of the reply sounds
	for m := next(t, c); m.Type != "talking_on"; m = next(t, c) {
		if m.Type != "text" && m.Type != "hearing" {
			t.Fatalf("got %+v, want the page told the user talked on", m)
		}
	}
	return request
}

func TestTalkingOnBeforeTheReplyReachesTheUserTakesItBackForAllTheWordsAsOneTurn(t *testing.T) {
	tts := &heldFirst{TextToSpeech: ttsfake.TextToSpeech{PacketsPerWord: packetsPerWord}, release: make(chan struct{})}
	defer close(tts.release)
	h, model, check, c := newJudgedHarness(t, speakingTwice, tts, nil)
	request := talkOn(t, c, check)
	speakFrames(c, 4+7)
	check.judge(t, true)
	got := until(t, c, "spoken")

	asked, _ := model.Requests()
	saved := h.turns.All()
	if len(saved) != 1 || saved[0].Words != both || saved[0].Seq != 1 || saved[0].RequestID.String() != request || got[len(got)-1].RequestID != request {
		t.Errorf("saved %+v, want one turn, the first's, with all the words", saved)
	}
	if last := asked[len(asked)-1]; last.NewWords != both || len(last.Conversation) != 0 {
		t.Errorf("asked last with %q after %+v, want all the words and nothing of the turn taken back", last.NewWords, last.Conversation)
	}
	if n := len(c.Played()); n != 4*packetsPerWord {
		t.Errorf("played %d packets, want only the second reply's %d: none of the first", n, 4*packetsPerWord)
	}
}

func TestSpeechOnceTheReplyIsHeardInterruptsItRatherThanTalkingOn(t *testing.T) {
	// Speech for 5 frames, quiet for 50 while the reply plays, then speech held for 20.
	d := detectionfake.Detection{Script: slices.Concat(slices.Repeat([]float32{0.9}, 5), make([]float32, 50), slices.Repeat([]float32{0.9}, 20))}
	h, _, check, c := newJudgedHarness(t, d, ttsfake.TextToSpeech{PacketsPerWord: slowWords}, nil)
	speakFrames(c, 12)
	check.judge(t, true)
	request := until(t, c, "done")[0].RequestID // settled: its reply reached the user
	playedAtLeast(t, c, 10)
	speakFrames(c, 43+20)
	for m := next(t, c); m.Type != "interrupted"; m = next(t, c) {
		if m.Type == "talking_on" {
			t.Fatalf("got %+v once the reply was heard, want it interrupted", m)
		}
	}
	speakFrames(c, 7)
	check.judge(t, true)
	until(t, c, "spoken")
	if saved := h.turns.All(); len(saved) != 2 || saved[0].RequestID.String() != request || saved[0].Status != conversation.TurnCut {
		t.Errorf("saved %+v, want the first turn cut, then the speech over it as the next", saved)
	}
}

// unremovable is the fake store of turns, failing to remove a turn.
type unremovable struct{ conversation.Turns }

func (unremovable) Remove(context.Context, conversation.Turn) error { return errors.New("disk full") }

// Against PostgreSQL, where a turn in flight past the turn deadline is cut off by the next to
// start: all the words, under the request ID of the turn that couldn't be taken back, would be
// refused as a repeat, and taken by the page as answered.
func TestATurnThatCantBeTakenBackPastItsDeadlineStillGivesAllTheWordsBack(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO workspaces (id, name) VALUES ($1, 'Harbour')", []any{workspace}},
		{"INSERT INTO agent.agents (id, workspace_id) VALUES ($1, $2)", []any{keeperID, workspace}},
		{`INSERT INTO agent.agent_versions (workspace_id, agent_id, version, name, description, reads, persona, content_hash)
			VALUES ($1, $2, 1, 'Lighthouse keeper', '', '{}', 'You answer every call from the sea.', 'hash')`, []any{workspace, keeperID}},
	} {
		if _, err := pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	tts := &heldFirst{TextToSpeech: ttsfake.TextToSpeech{PacketsPerWord: packetsPerWord}, release: make(chan struct{})}
	defer close(tts.release)
	check := judging{asked: make(chan chan bool, 4)}
	h := newStoredTurnHarness(t, postgres.New(pool), unremovable{postgres.NewTurns(pool)}, &modelfake.Model{Pieces: replyPieces}, tts,
		slog.New(slog.DiscardHandler), speakingTwice, saying, check)
	c := h.connect(t)
	request := talkOn(t, c, check)
	// The user talks on past the turn deadline, the turn that couldn't be taken back still in flight.
	if _, err := pool.Exec(ctx, "UPDATE turns SET created_at = created_at - interval '1 hour'"); err != nil {
		t.Fatalf("age the turn: %v", err)
	}
	speakFrames(c, 4+7)
	check.judge(t, true)
	got := until(t, c, "refused")
	if refused := got[len(got)-1]; refused.RequestID != request || refused.Reason != "busy" {
		t.Errorf("got %+v, want all the words refused as busy, so the page puts them back in the box", refused)
	}
	var merged int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM turns WHERE user_text = $1", both).Scan(&merged); err != nil || merged != 0 {
		t.Errorf("%d turns saved with all the words (%v), want none", merged, err)
	}
}

func TestATurnThatCantBeTakenBackGivesAllTheWordsBackAndSavesNothingMore(t *testing.T) {
	tts := &heldFirst{TextToSpeech: ttsfake.TextToSpeech{PacketsPerWord: packetsPerWord}, release: make(chan struct{})}
	defer close(tts.release)
	h, _, check, c := newJudgedHarness(t, speakingTwice, tts, func(t conversation.Turns) conversation.Turns { return unremovable{t} })
	request := talkOn(t, c, check)
	speakFrames(c, 4+7)
	check.judge(t, true)
	got := until(t, c, "refused")
	if refused := got[len(got)-1]; refused.RequestID != request || refused.Reason != "busy" || !slices.ContainsFunc(got, func(m message) bool { return m.Type == "hearing" && m.Final && m.Text == both }) {
		t.Errorf("got %+v, want all the words shown, then refused as busy, so the page puts them back in the box", got)
	}
	if saved := h.turns.All(); len(saved) != 1 || saved[0].Status != conversation.TurnInFlight || saved[0].Words == both {
		t.Errorf("saved %+v, want only the first words, still in flight, as they were", saved)
	}
}
