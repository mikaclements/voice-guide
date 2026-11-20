package speech_test

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/conversation"
	modelfake "github.com/mikaclements/voice-guide/internal/conversation/model/fake"
	endofturnfake "github.com/mikaclements/voice-guide/internal/speech/endofturn/fake"
	"github.com/mikaclements/voice-guide/internal/speech/transport/fake"
	ttsfake "github.com/mikaclements/voice-guide/internal/speech/tts/fake"
)

// blockJSON is one block of screen input, as the page sends it.
func blockJSON(name, content string) string {
	return fmt.Sprintf(`{"name":%q,"content":%q}`, name, content)
}

// screenMessage is the page sending blocks, JSON objects, on their own as screen message n.
func screenMessage(n int, blocks ...string) string {
	return fmt.Sprintf(`{"type":"screen","n":%d,"screen_input":[%s]}`, n, strings.Join(blocks, ","))
}

// showScreen sends screen message n and returns once speech has read it: speech reads the data
// channel in order, so the message it refuses right after has come back.
func showScreen(t *testing.T, c *fake.Connection, n int, blocks ...string) {
	t.Helper()
	c.Say(screenMessage(n, blocks...))
	c.Say(`{}`)
	if m := next(t, c); m.Type != "refused" || m.Reason != "unreadable" {
		t.Fatalf("got %+v after screen message %d, want it kept and only the message after it refused", m, n)
	}
}

// screenOf is a turn's screen input by block name, so tests don't depend on the blocks' order.
func screenOf(turn conversation.Turn) map[string]string {
	m := make(map[string]string, len(turn.ScreenInput))
	for _, b := range turn.ScreenInput {
		m[b.Name] = b.Content
	}
	return m
}

var withBoard = map[string]string{"board": "Cache -> Database"}

// screenTaken returns the screen_taken of the first message about request carrying one.
func screenTaken(got []message, request string) int {
	for _, m := range got {
		if m.RequestID == request && m.ScreenTaken != 0 {
			return m.ScreenTaken
		}
	}
	return 0
}

func newScreenHarness(t *testing.T) (turnHarness, *fake.Connection) {
	t.Helper()
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1})
	return h, h.connect(t)
}

func TestAScreenChangeGoesWithTheNextTypedTurnOnly(t *testing.T) {
	h, c := newScreenHarness(t)
	first, second := uuid.NewV7(), uuid.NewV7()
	showScreen(t, c, 1, blockJSON("board", "Cache -> Database"))
	c.Say(turnMessage(first, "Is this right?"))
	got := until(t, c, "spoken")
	for _, m := range got {
		if (m.Type == "done") != (m.ScreenTaken == 1) {
			t.Errorf("got %+v, want only done saying screen message 1 went with the turn, once it is saved", m)
		}
	}
	c.Say(turnMessage(second, "And now?"))
	if screenTaken(until(t, c, "spoken"), second.String()) != 0 {
		t.Error("the next turn said screen messages went with it, want none: the change went with the turn before")
	}
	if saved := h.turns.All(); len(saved) != 2 || !maps.Equal(screenOf(saved[0]), withBoard) || len(saved[1].ScreenInput) != 0 {
		t.Errorf("saved %+v, want the board with the first turn alone", saved)
	}
}

func TestOnlyTheNewestBlockOfEachNameWaitsAndATurnsOwnBlockWins(t *testing.T) {
	h, c := newScreenHarness(t)
	showScreen(t, c, 1, blockJSON("board", "Cache"), blockJSON("notes", "Read-heavy"))
	showScreen(t, c, 2, blockJSON("board", "Cache -> Database"), blockJSON("diagram", "Ahead"))
	id := uuid.NewV7()
	c.Say(fmt.Sprintf(`{"type":"turn","request_id":"%s","words":"Is this right?","screen_input":[%s]}`, id, blockJSON("diagram", "Own")))
	if taken := screenTaken(until(t, c, "spoken"), id.String()); taken != 2 {
		t.Errorf("the turn said screen messages up to %d went with it, want 2", taken)
	}
	want := map[string]string{"board": "Cache -> Database", "notes": "Read-heavy", "diagram": "Own"}
	if saved := h.turns.All(); len(saved) != 1 || !maps.Equal(screenOf(saved[0]), want) {
		t.Errorf("saved %+v, want the newer board, the notes and the turn's own diagram", saved)
	}
}

func TestAScreenChangeGoesWithTheAgentsOpening(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1})
	s, _, err := h.conv.StartSession(context.Background(), product, conversation.StartRequest{Agent: openerID, RequestID: uuid.NewV7(), User: "aiko-42"})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	h.session = s.ID
	c := h.connect(t)
	showScreen(t, c, 1, blockJSON("board", "Cache -> Database"))
	open := uuid.NewV7()
	c.Say(openingMessage(open))
	if taken := screenTaken(until(t, c, "spoken"), open.String()); taken != 1 {
		t.Errorf("the opening said screen messages up to %d went with it, want 1", taken)
	}
	if saved := h.turns.All(); len(saved) != 1 || !saved[0].Opening || !maps.Equal(screenOf(saved[0]), withBoard) {
		t.Errorf("saved %+v, want the opening with the board", saved)
	}
}

func TestAScreenChangeGoesWithTheNextSpokenTurn(t *testing.T) {
	h := newHearingTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1},
		slog.New(slog.DiscardHandler), nil, speechThenQuiet, saying, endofturnfake.EndOfTurn{Answer: true})
	c := h.connect(t)
	showScreen(t, c, 1, blockJSON("board", "Cache -> Database"))
	speakFrames(c, 12)
	got := until(t, c, "spoken")
	if taken := screenTaken(got, got[0].RequestID); taken != 1 {
		t.Errorf("the spoken turn said screen messages up to %d went with it, want 1", taken)
	}
	if saved := h.turns.All(); len(saved) != 1 || saved[0].Words != "Where should we start?" || !maps.Equal(screenOf(saved[0]), withBoard) {
		t.Errorf("saved %+v, want the spoken words with the board", saved)
	}
}

func TestARefusedTurnLeavesTheScreenChangeForTheNext(t *testing.T) {
	h, c := newScreenHarness(t)
	taken, next := uuid.NewV7(), uuid.NewV7()
	c.Say(turnMessage(taken, "Where should we start?"))
	until(t, c, "spoken")
	showScreen(t, c, 1, blockJSON("board", "Cache -> Database"))
	c.Say(turnMessage(taken, "Again?"))
	if got := until(t, c, "refused"); got[0].Reason != "repeated" || got[0].ScreenTaken != 0 {
		t.Errorf("got %+v, want the repeat refused, saying no screen message went with it", got[0])
	}
	c.Say(turnMessage(next, "Is this right?"))
	if n := screenTaken(until(t, c, "spoken"), next.String()); n != 1 {
		t.Errorf("the next turn said screen messages up to %d went with it, want 1", n)
	}
	if saved := h.turns.All(); len(saved) != 2 || len(saved[0].ScreenInput) != 0 || !maps.Equal(screenOf(saved[1]), withBoard) {
		t.Errorf("saved %+v, want the board with the turn after the refusal", saved)
	}
}

// The screen message and the second turn arrive while the first is being taken: the change may
// go with either, but with exactly one.
func TestAScreenChangeArrivingWhileATurnIsTakenGoesWithItOrTheNextOnce(t *testing.T) {
	h, c := newScreenHarness(t)
	first, second := uuid.NewV7(), uuid.NewV7()
	c.Say(turnMessage(first, "Where should we start?"))
	c.Say(screenMessage(1, blockJSON("board", "Cache -> Database")))
	c.Say(turnMessage(second, "Is this right?"))
	for m := next(t, c); m.Type != "spoken" || m.RequestID != second.String(); {
		m = next(t, c)
	}
	if saved := h.turns.All(); len(saved) != 2 || maps.Equal(screenOf(saved[0]), withBoard) == maps.Equal(screenOf(saved[1]), withBoard) {
		t.Errorf("saved %+v, want the board with exactly one of the two turns", saved)
	}
}

func TestATurnTakenBackHandsItsScreenChangeToTheTurnTakingItsPlace(t *testing.T) {
	tts := &heldFirst{TextToSpeech: ttsfake.TextToSpeech{PacketsPerWord: packetsPerWord}, release: make(chan struct{})}
	defer close(tts.release)
	h, _, check, c := newJudgedHarness(t, speakingTwice, tts, nil)
	showScreen(t, c, 1, blockJSON("board", "Cache -> Database"))
	request := talkOn(t, c, check)
	speakFrames(c, 4+7)
	check.judge(t, true)
	if taken := screenTaken(until(t, c, "spoken"), request); taken != 1 {
		t.Errorf("the turn taking its place said screen messages up to %d went with it, want 1", taken)
	}
	if saved := h.turns.All(); len(saved) != 1 || saved[0].Words != both || !maps.Equal(screenOf(saved[0]), withBoard) {
		t.Errorf("saved %+v, want one turn with all the words and the board", saved)
	}
}

// The harness takes screen input of up to 1,000 characters.
func TestAScreenMessageThatCantBeUsedIsRefusedAndNothingOfItWaits(t *testing.T) {
	h, c := newScreenHarness(t)
	board := strings.Repeat("b", 600)
	showScreen(t, c, 1, blockJSON("board", board))
	for i, tc := range []struct{ name, blocks string }{
		{"a block over the limit", blockJSON("diagram", strings.Repeat("d", 1001))},
		{"a block over the limit with the blocks waiting", blockJSON("notes", strings.Repeat("n", 600))},
		{"a badly formed name", blockJSON("Notes!", "Read-heavy")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c.Say(screenMessage(i+2, tc.blocks))
			if m := next(t, c); m.Type != "screen_refused" || m.N != i+2 || m.Detail == "" {
				t.Errorf("got %+v, want screen message %d refused, saying why", m, i+2)
			}
		})
	}
	id := uuid.NewV7()
	c.Say(turnMessage(id, "Is this right?"))
	if taken := screenTaken(until(t, c, "spoken"), id.String()); taken != 1 {
		t.Errorf("the turn said screen messages up to %d went with it, want only the one kept, 1", taken)
	}
	if saved := h.turns.All(); len(saved) != 1 || !maps.Equal(screenOf(saved[0]), map[string]string{"board": board}) {
		t.Errorf("saved %d turns, want one with only the board kept before the refusals", len(saved))
	}
}

// A block stays held while a turn carries it, so what is held never passes what a turn may take,
// whether that turn is saved or not; once it is saved, the block is let go.
func TestABlockATurnCarriesCountsAgainstTheLimitUntilTheTurnIsSaved(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces, Pace: time.Hour}, ttsfake.TextToSpeech{PacketsPerWord: 1})
	c := h.connect(t)
	board, notes := blockJSON("board", strings.Repeat("b", 600)), blockJSON("notes", strings.Repeat("n", 600))
	showScreen(t, c, 1, board)
	c.Say(turnMessage(uuid.NewV7(), "Is this right?"))
	savedAtLeast(t, h.turns, 1) // taken with the board, its reply still to come
	c.Say(screenMessage(2, notes))
	if m := next(t, c); m.Type != "screen_refused" || m.N != 2 {
		t.Errorf("got %+v, want the notes refused: with the board the turn carries, over the limit", m)
	}
	c.Say(turnMessage(uuid.NewV7(), "Actually, wait."))
	until(t, c, "interrupted") // the first turn saved, cut, with the board
	showScreen(t, c, 3, notes)
	if saved := h.turns.All(); !maps.Equal(screenOf(saved[0]), map[string]string{"board": strings.Repeat("b", 600)}) {
		t.Errorf("saved %+v, want the first turn with the board", saved)
	}
}
