package speech_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	agentsfake "github.com/mikaclements/voice-guide/internal/agents/store/fake"
	"github.com/mikaclements/voice-guide/internal/conversation"
	modelfake "github.com/mikaclements/voice-guide/internal/conversation/model/fake"
	sessionsfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/speech"
	detectionfake "github.com/mikaclements/voice-guide/internal/speech/detection/fake"
	"github.com/mikaclements/voice-guide/internal/speech/transport/fake"
	ttsfake "github.com/mikaclements/voice-guide/internal/speech/tts/fake"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	itemsfake "github.com/mikaclements/voice-guide/internal/usercontext/store/fake"
)

// The fake model's reply: two pieces, four words.
var replyPieces = []string{"Tell me", " about Harbourline."}

// packetsPerWord makes a four-word reply 40 packets, 0.8s of speech, so the
// reply's text has long arrived when its audio is still playing.
const packetsPerWord = 10

// turnHarness is speech's service over conversation's fakes, with one session
// of the caller's.
type turnHarness struct {
	svc       *speech.Service
	conv      *conversation.Service
	transport *fake.Transport
	turns     *sessionsfake.Turns
	model     conversation.Model
	session   uuid.UUID
	// caller is the session's user, as their browser calls.
	caller access.Caller
}

func newTurnHarness(t *testing.T, model *modelfake.Model, tts speech.TextToSpeech) turnHarness {
	t.Helper()
	return newLoggingTurnHarness(t, model, tts, slog.New(slog.DiscardHandler), nil)
}

// newLoggingTurnHarness logs to log, and saves turns through wrap's store when wrap is set.
func newLoggingTurnHarness(t *testing.T, model conversation.Model, tts speech.TextToSpeech, log *slog.Logger, wrap func(conversation.Turns) conversation.Turns) turnHarness {
	t.Helper()
	return newHearingTurnHarness(t, model, tts, log, wrap, detectionfake.Detection{}, nil, nil)
}

// newHearingTurnHarness also notices the user speak with d, hears them with stt and asks eot
// whether they have finished.
func newHearingTurnHarness(t *testing.T, model conversation.Model, tts speech.TextToSpeech, log *slog.Logger, wrap func(conversation.Turns) conversation.Turns,
	d speech.SpeechDetection, stt speech.SpeechToText, eot speech.EndOfTurn,
) turnHarness {
	t.Helper()
	turns := sessionsfake.NewTurns()
	var store conversation.Turns = turns
	if wrap != nil {
		store = wrap(turns)
	}
	h := newStoredTurnHarness(t, sessionsfake.New(), store, model, tts, log, d, stt, eot)
	h.turns = turns
	return h
}

// newStoredTurnHarness keeps sessions and turns in the stores given.
func newStoredTurnHarness(t *testing.T, sessions conversation.Sessions, store conversation.Turns, model conversation.Model, tts speech.TextToSpeech, log *slog.Logger,
	d speech.SpeechDetection, stt speech.SpeechToText, eot speech.EndOfTurn,
) turnHarness {
	t.Helper()
	versions := agentsfake.New()
	if _, err := agents.Publish(context.Background(), versions, workspace, []agents.Agent{{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea."}}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	conv := conversation.NewService(agents.NewCatalog(versions), sessions, store, model, usercontext.NewService(itemsfake.New()),
		conversation.Settings{UserContextChars: 1000, SessionContextChars: 1000, ScreenInputChars: 1000, WordsChars: 1000, TurnDeadline: time.Minute, Model: "claude-test"})
	s, _, err := conv.StartSession(context.Background(), product, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), User: "aiko-42"})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	transport := fake.New()
	svc := speech.NewService(conv, transport, tts, d, stt, eot, speech.Settings{MaxConnections: 10, Voice: "af_heart", TextToSpeech: "fake", Listening: startingListening}, log)
	t.Cleanup(func() { closeService(t, svc) })
	return turnHarness{svc: svc, conv: conv, transport: transport, model: model, session: s.ID, caller: access.Caller{Workspace: workspace, User: s.User, Expires: expires}}
}

// connect opens the session's audio connection and returns its browser side.
func (h turnHarness) connect(t *testing.T) *fake.Connection {
	t.Helper()
	if _, _, err := h.svc.Connect(context.Background(), h.caller, h.session, "offer"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return h.transport.Conn(h.transport.Answered() - 1)
}

// message is what speech sends the browser, with every field any message has.
type message struct {
	Type         string  `json:"type"`
	RequestID    string  `json:"request_id"`
	Text         string  `json:"text"`
	Final        bool    `json:"final"`
	TurnID       string  `json:"turn_id"`
	Seq          int     `json:"seq"`
	Reason       string  `json:"reason"`
	Failed       bool    `json:"failed"`
	Model        string  `json:"model"`
	Heard        *string `json:"heard"`
	FirstAudioMS int64   `json:"first_audio_ms"`
}

// next returns the next message speech sends the browser.
func next(t *testing.T, c *fake.Connection) message {
	t.Helper()
	select {
	case raw := <-c.Sent():
		var m message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("speech sent %s, not JSON: %v", raw, err)
		}
		return m
	case <-time.After(wait):
		t.Fatalf("waited %s for a message from speech", wait)
		return message{}
	}
}

// until returns the messages speech sends up to and including one of type typ.
func until(t *testing.T, c *fake.Connection, typ string) []message {
	t.Helper()
	var got []message
	for {
		m := next(t, c)
		got = append(got, m)
		if m.Type == typ {
			return got
		}
	}
}

func turnMessage(requestID uuid.UUID, words string) string {
	return `{"type":"turn","request_id":"` + requestID.String() + `","words":"` + words + `"}`
}

func TestATypedTurnIsAnsweredInTextAtOnceAndSpokenInRealTime(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: packetsPerWord})
	c := h.connect(t)
	id := uuid.NewV7()
	c.Say(turnMessage(id, "Where should we start?"))

	answered := until(t, c, "done")
	playedByDone := len(c.Played())
	var text []string
	for _, m := range answered[:len(answered)-1] {
		if m.Type != "text" || m.RequestID != id.String() {
			t.Errorf("got %+v before done, want only the reply's text for request %s", m, id)
		}
		text = append(text, m.Text)
	}
	if strings.Join(text, "") != strings.Join(replyPieces, "") {
		t.Errorf("the browser got the text %q, want the reply %q", text, replyPieces)
	}
	done := answered[len(answered)-1]
	saved := h.turns.All()
	if len(saved) != 1 || done.TurnID != saved[0].ID.String() || done.Seq != 1 || done.Model != "fake/claude-test" || saved[0].Status != conversation.TurnAnswered {
		t.Errorf("done = %+v with saved turns %+v, want the answered turn's ID, number 1 and what answered it", done, saved)
	}
	if spoken := next(t, c); spoken.Type != "spoken" || spoken.RequestID != id.String() || spoken.Failed {
		t.Errorf("after done got %+v, want spoken for request %s once the audio has played", spoken, id)
	}

	played := c.Played()
	if want := 4 * packetsPerWord; len(played) != want {
		t.Fatalf("played %d packets, want %d: every packet text-to-speech made", len(played), want)
	}
	if playedByDone >= len(played) {
		t.Errorf("all %d packets had played when the text was done: the text waited for the audio", playedByDone)
	}
	// A packet holds 20 ms of audio, so packets sent faster than that would
	// pile up in the browser, and the reply would end before it was heard.
	if took, least := played[len(played)-1].At.Sub(played[0].At), time.Duration(len(played)-1)*speech.PacketDuration; took < least {
		t.Errorf("%d packets went out over %s, want at least %s: one every %s", len(played), took, least, speech.PacketDuration)
	}
}

// playedAtLeast waits until the browser has had at least n packets, and returns how many.
func playedAtLeast(t *testing.T, c *fake.Connection, n int) int {
	t.Helper()
	deadline := time.After(wait)
	for {
		played, more := c.Watch()
		if len(played) >= n {
			return len(played)
		}
		select {
		case <-more:
		case <-deadline:
			t.Fatalf("waited %s for %d packets to play, %d played", wait, n, len(played))
		}
	}
}

// savedAtLeast waits until the store holds at least n turns, in flight or not, and returns them.
func savedAtLeast(t *testing.T, turns *sessionsfake.Turns, n int) []conversation.Turn {
	t.Helper()
	deadline := time.After(wait)
	for {
		saved, changed := turns.Watch()
		if len(saved) >= n {
			return saved
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("waited %s for %d turns to be saved, %d saved", wait, n, len(saved))
		}
	}
}

// slowWords makes each word of the reply 25 packets, half a second, so a test
// can cut in early in its second phrase with time to spare on either side.
const slowWords = 25

func TestATurnSentWhileTheAgentSpeaksStopsItAndSavesWhatWasHeard(t *testing.T) {
	// The second turn waits to read the conversation until the first reply's
	// packets are counted, or on a slow machine its first packets join the count.
	held := heldPrompt{armed: &atomic.Bool{}, once: &sync.Once{}, release: make(chan struct{})}
	h := newLoggingTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: slowWords}, slog.New(slog.DiscardHandler),
		func(s conversation.Turns) conversation.Turns { held.Turns = s; return held })
	c := h.connect(t)
	first, second := uuid.NewV7(), uuid.NewV7()
	c.Say(turnMessage(first, "Where should we start?"))
	until(t, c, "done")
	// "Tell me" has played and " about Harbourline." has begun: its first word isn't heard yet.
	cutIn := playedAtLeast(t, c, 2*slowWords+5)
	held.armed.Store(true)
	c.Say(turnMessage(second, "Hold on."))

	got := until(t, c, "interrupted")
	stopped := len(c.Played())
	close(held.release)
	if m := got[len(got)-1]; len(got) != 1 || m.RequestID != first.String() || m.Heard == nil || *m.Heard != "Tell me" {
		t.Errorf("after cutting in got %+v, want only interrupted for the first turn, heard up to %q", got, "Tell me")
	}
	if stopped-cutIn > 5 {
		t.Errorf("%d packets played after cutting in, want the audio stopped at once: at most 5, 0.1s", stopped-cutIn)
	}
	if got := until(t, c, "spoken"); got[len(got)-1].RequestID != second.String() {
		t.Errorf("then got %+v, want the second turn answered and spoken", got)
	}
	if n := len(c.Played()); n != stopped+4*slowWords {
		t.Errorf("%d packets played in all, want the first reply's %d and the second's %d: nothing of the first after it stopped", n, stopped, 4*slowWords)
	}
	saved := h.turns.All()
	if len(saved) != 2 || saved[0].Status != conversation.TurnCut || saved[0].HeardReply() != "Tell me" || saved[1].Status != conversation.TurnAnswered {
		t.Errorf("saved %+v, want the first cut after %q and the second answered", saved, "Tell me")
	}
}

func TestATurnSentBeforeAnySoundIsTakenAfterTheFirstIsSavedWithNothingHeard(t *testing.T) {
	// Each piece of the reply takes far longer than the test, so nothing is said before the user cuts in.
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces, Pace: time.Hour}, ttsfake.TextToSpeech{PacketsPerWord: 1})
	c := h.connect(t)
	first, second := uuid.NewV7(), uuid.NewV7()
	c.Say(turnMessage(first, "Where should we start?"))
	savedAtLeast(t, h.turns, 1) // the first turn is saved in flight before any text
	c.Say(turnMessage(second, "Actually, one thing first."))

	got := until(t, c, "interrupted")
	if len(got) != 2 || got[0].Type != "done" || got[0].RequestID != first.String() || got[1].RequestID != first.String() || got[1].Heard == nil || *got[1].Heard != "" {
		t.Errorf("got %+v, want the first turn done, then interrupted with nothing heard", got)
	}
	// The second turn starts once the first is saved.
	if saved := savedAtLeast(t, h.turns, 2); saved[0].Words != "Where should we start?" || saved[0].Reply != "" || saved[0].Status != conversation.TurnCut || saved[1].RequestID != second {
		t.Errorf("saved %+v, want the first turn's words cut with no reply, then the second turn", saved)
	}
}

func TestTurnsSentOneAfterAnotherAreAllTakenInOrder(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: packetsPerWord})
	c := h.connect(t)
	ids := []uuid.UUID{uuid.NewV7(), uuid.NewV7(), uuid.NewV7()}
	for i, id := range ids {
		c.Say(turnMessage(id, fmt.Sprintf("Question %d?", i+1)))
	}

	var endings []string
	for _, m := range until(t, c, "spoken") {
		if m.Type != "text" {
			endings = append(endings, m.Type+" "+m.RequestID)
		}
	}
	want := []string{"done " + ids[0].String(), "interrupted " + ids[0].String(), "done " + ids[1].String(), "interrupted " + ids[1].String(), "done " + ids[2].String(), "spoken " + ids[2].String()}
	if !slices.Equal(endings, want) {
		t.Errorf("got %q, want each turn ended in order, the last one spoken: %q", endings, want)
	}
	saved := h.turns.All()
	for i, id := range ids {
		if len(saved) != 3 || saved[i].RequestID != id || saved[i].Seq != i+1 {
			t.Fatalf("saved %+v, want the three turns in the order sent", saved)
		}
	}
}

func TestATurnSentOnceTheReplyHasPlayedLeavesItWhole(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1})
	c := h.connect(t)
	first, second := uuid.NewV7(), uuid.NewV7()
	c.Say(turnMessage(first, "Where should we start?"))
	until(t, c, "spoken")
	c.Say(turnMessage(second, "Shall we go on?"))
	got := until(t, c, "spoken")
	for _, m := range got {
		if m.Type == "interrupted" {
			t.Errorf("got %+v, want no interrupt: the first reply had all played", m)
		}
	}
	if saved := h.turns.All(); len(saved) != 2 || saved[0].Status != conversation.TurnAnswered || saved[1].Status != conversation.TurnAnswered {
		t.Errorf("saved %+v, want both answered whole", saved)
	}
}

func TestTurnsTheSessionCantTakeAreRefusedWithTheReason(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1})
	c := h.connect(t)
	taken, fresh := uuid.NewV7(), uuid.NewV7()
	c.Say(turnMessage(taken, "Where should we start?"))
	until(t, c, "spoken")
	spoken := len(c.Played())
	for _, tc := range []struct {
		name, msg, request, reason string
	}{
		{"a message that isn't JSON", "not json", "", "unreadable"},
		{"a turn without a request ID", `{"type":"turn","words":"Hi"}`, "", "unreadable"},
		{"a message of another type", `{"type":"wave","request_id":"` + taken.String() + `"}`, taken.String(), "unreadable"},
		{"a turn with no words", turnMessage(fresh, ""), fresh.String(), "unusable"},
		{"a request ID that already took a turn", turnMessage(taken, "Again?"), taken.String(), "repeated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c.Say(tc.msg)
			if m := next(t, c); m.Type != "refused" || m.RequestID != tc.request || m.Reason != tc.reason {
				t.Errorf("got %+v, want refused for %q as %s", m, tc.request, tc.reason)
			}
		})
	}
	if n := len(h.turns.All()); n != 1 {
		t.Errorf("%d turns saved, want only the first", n)
	}
	// A repeat is acknowledged, never answered again: nothing more was said or sent.
	if n := len(c.Played()); n != spoken {
		t.Errorf("%d packets played after the refusals, want the first reply's %d: a repeat was spoken again", n, spoken)
	}
	select {
	case m := <-c.Sent():
		t.Errorf("after the refusals speech sent %s, want nothing", m)
	default:
	}
}

func openingMessage(requestID uuid.UUID) string {
	return `{"type":"turn","request_id":"` + requestID.String() + `","opening":true}`
}

// The harness's agent waits for the user, so its session takes no opening.
func TestAnOpeningTheSessionDoesntTakeIsRefusedWithItsReasonAndNothingSaid(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1})
	c := h.connect(t)
	open := uuid.NewV7()
	c.Say(openingMessage(open))
	if m := next(t, c); m.Type != "refused" || m.RequestID != open.String() || m.Reason != "opening-refused" {
		t.Errorf("got %+v, want the opening refused as opening-refused", m)
	}
	if saved, played := h.turns.All(), c.Played(); len(saved) != 0 || len(played) != 0 {
		t.Errorf("saved %+v and played %d packets, want nothing", saved, len(played))
	}
}

// The user spoke or typed before the page sent the opening: the agent answers them.
func TestAnOpeningSentWhileATurnIsTakenIsRefusedWithoutCuttingInOnIt(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: packetsPerWord})
	c := h.connect(t)
	first, open := uuid.NewV7(), uuid.NewV7()
	c.Say(turnMessage(first, "Where should we start?"))
	c.Say(openingMessage(open))

	var endings []string
	for _, m := range until(t, c, "spoken") {
		if m.Type != "text" {
			endings = append(endings, m.Type+" "+m.Reason+" "+m.RequestID)
		}
	}
	want := []string{"refused opening-refused " + open.String(), "done  " + first.String(), "spoken  " + first.String()}
	if !slices.Equal(endings, want) {
		t.Errorf("got %q, want the opening refused and the turn answered whole: %q", endings, want)
	}
	if saved := h.turns.All(); len(saved) != 1 || saved[0].Status != conversation.TurnAnswered {
		t.Errorf("saved %+v, want the user's turn alone, answered", saved)
	}
}

func TestSpeechFailingLeavesTheReplysTextFlowing(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1, Fail: errors.New("text-to-speech is down")})
	c := h.connect(t)
	id := uuid.NewV7()
	c.Say(turnMessage(id, "Where should we start?"))
	got := until(t, c, "done")
	var text strings.Builder
	for _, m := range got {
		text.WriteString(m.Text)
	}
	if text.String() != strings.Join(replyPieces, "") {
		t.Errorf("the browser got %q, want the whole reply as text though no audio was made", text.String())
	}
	if m := next(t, c); m.Type != "spoken" || !m.Failed {
		t.Errorf("got %+v, want spoken with failed set, so the page can say the audio stopped", m)
	}
}

func TestATurnInFlightIsCancelledAndSavedLostWhenTheConnectionEnds(t *testing.T) {
	for name, end := range map[string]func(h turnHarness){
		"the browser hangs up":    func(h turnHarness) { h.transport.EndAll() },
		"the server stops":        func(h turnHarness) { h.svc.Close() },
		"a newer one replaces it": func(h turnHarness) { _, _, _ = h.svc.Connect(context.Background(), h.caller, h.session, "offer") },
	} {
		t.Run(name, func(t *testing.T) {
			// The model sends its first piece, then takes far longer than the test for the next.
			model := &modelfake.Model{Pieces: []string{"Tell me", " about", " Harbourline."}, Pace: time.Hour}
			h := newTurnHarness(t, model, ttsfake.TextToSpeech{PacketsPerWord: 1})
			c := h.connect(t)
			c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
			savedAtLeast(t, h.turns, 1) // the turn is saved in flight before any text
			end(h)
			closeService(t, h.svc) // returns once every turn is saved
			if saved := h.turns.All(); saved[0].Status != conversation.TurnLost {
				t.Errorf("the turn was saved %s, want lost", saved[0].Status)
			}
			if _, stopped := model.Requests(); stopped != 1 {
				t.Errorf("the model's reply was stopped %d times, want once", stopped)
			}
		})
	}
}

// heldFinish is a turn store that tells started once its one turn is saved in
// flight, and whose Finish waits until release is closed, telling entered
// when it starts.
type heldFinish struct {
	conversation.Turns
	started, entered, release chan struct{}
}

func (h heldFinish) Start(ctx context.Context, t conversation.Turn, after int, deadline time.Duration) (int, error) {
	seq, err := h.Turns.Start(ctx, t, after, deadline)
	if err == nil {
		close(h.started)
	}
	return seq, err
}

func (h heldFinish) Finish(ctx context.Context, t conversation.Turn) error {
	close(h.entered)
	<-h.release
	return h.Turns.Finish(ctx, t)
}

// unblocked is how long Close gets to return while a turn is still being
// saved: far longer than Close takes when it doesn't wait (microseconds),
// short enough to keep the test quick.
const unblocked = 50 * time.Millisecond

func TestCloseReturnsOnlyOnceTheTurnInFlightIsSaved(t *testing.T) {
	started, entered, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	// The model takes far longer than the test to send anything.
	model := &modelfake.Model{Pieces: replyPieces, Pace: time.Hour}
	h := newLoggingTurnHarness(t, model, ttsfake.TextToSpeech{PacketsPerWord: 1}, slog.New(slog.DiscardHandler),
		func(s conversation.Turns) conversation.Turns { return heldFinish{s, started, entered, release} })
	c := h.connect(t)
	c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
	select {
	case <-started:
	case <-time.After(wait):
		t.Fatalf("waited %s for the turn to be saved in flight", wait)
	}
	closed := make(chan struct{})
	go func() { h.svc.Close(); close(closed) }()
	select {
	case <-entered: // the turn, cancelled, is being saved lost
	case <-time.After(wait):
		t.Fatal("the turn in flight wasn't saved when the server stopped")
	}
	select {
	case <-closed:
		t.Error("Close returned while the turn in flight was still being saved")
	case <-time.After(unblocked):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(wait):
		t.Fatal("Close didn't return once the turn was saved")
	}
}

// logged keeps the attributes of each log record with the given message.
type logged struct {
	message string
	mu      sync.Mutex
	records []map[string]slog.Value
	wrote   chan struct{} // made by a wait, closed when the next record is logged
}

func (l *logged) Enabled(context.Context, slog.Level) bool { return true }
func (l *logged) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *logged) WithGroup(string) slog.Handler            { return l }

func (l *logged) Handle(_ context.Context, r slog.Record) error {
	if r.Message != l.message {
		return nil
	}
	attrs := map[string]slog.Value{}
	r.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value; return true })
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, attrs)
	if l.wrote != nil {
		close(l.wrote)
		l.wrote = nil
	}
	return nil
}

func (l *logged) first() (map[string]slog.Value, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.records) == 0 {
		return nil, false
	}
	return l.records[0], true
}

func TestASpokenTurnLogsEachStageFromTheWordsArriving(t *testing.T) {
	log := &logged{message: "turn spoken"}
	// Each piece takes 30ms, so each stage comes measurably after the words.
	h := newLoggingTurnHarness(t, &modelfake.Model{Pieces: replyPieces, Pace: 30 * time.Millisecond}, ttsfake.TextToSpeech{PacketsPerWord: 1}, slog.New(log), nil)
	c := h.connect(t)
	c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
	until(t, c, "spoken")
	closeService(t, h.svc) // returns once the turn has logged its stages
	got, ok := log.first()
	if !ok {
		t.Fatal("the turn's stage times weren't logged")
	}
	if got["turn"].String() != h.turns.All()[0].ID.String() || got["model"].String() != "fake/claude-test" || got["text_to_speech"].String() != "fake" {
		t.Errorf("logged %v, want the turn's ID and what served the model and text-to-speech", got)
	}
	var last int64
	for _, stage := range []string{"model_first_text_ms", "first_phrase_ready_ms", "first_audio_sent_ms"} {
		v, ok := got[stage]
		if !ok || v.Kind() != slog.KindInt64 || v.Int64() < max(last, 30) {
			t.Errorf("%s = %v, want milliseconds from the words arriving: at least the model's 30, and none before the stage before", stage, v)
		}
		last = v.Int64()
	}
}

func TestAReplyTheModelDidntFinishIsLostWithItsTurnThenSpoken(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces, Fail: "overloaded"}, ttsfake.TextToSpeech{PacketsPerWord: 1})
	c := h.connect(t)
	c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
	got := until(t, c, "lost")
	saved := h.turns.All()
	if lost := got[len(got)-1]; len(saved) != 1 || lost.TurnID != saved[0].ID.String() || lost.Seq != 1 || saved[0].Status != conversation.TurnLost {
		t.Errorf("lost = %+v with saved %+v, want the lost turn's ID and number", lost, saved)
	}
	if m := next(t, c); m.Type != "spoken" {
		t.Errorf("after lost got %+v, want spoken: the text that came was still said", m)
	}
}

// brokenStart is a turn store that can't start a turn, as when the database is down.
type brokenStart struct{ conversation.Turns }

func (brokenStart) Start(context.Context, conversation.Turn, int, time.Duration) (int, error) {
	return 0, errors.New("the database is down")
}

func TestATurnThatFailsBeforeItsReplyIsFailedSoThePageSendsItAgain(t *testing.T) {
	h := newLoggingTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1}, slog.New(slog.DiscardHandler),
		func(s conversation.Turns) conversation.Turns { return brokenStart{s} })
	c := h.connect(t)
	id := uuid.NewV7()
	c.Say(turnMessage(id, "Where should we start?"))
	if m := next(t, c); m.Type != "failed" || m.RequestID != id.String() {
		t.Errorf("got %+v, want failed for request %s", m, id)
	}
	c.Say(turnMessage(id, "Where should we start?"))
	if m := next(t, c); m.Type != "failed" {
		t.Errorf("sending it again got %+v, want failed again, not busy: the agent was freed", m)
	}
}

func TestAConnectionEndingWhileTheReplyPlaysStopsTheAudioAndKeepsTheTurn(t *testing.T) {
	for name, end := range map[string]func(h turnHarness){
		"the browser hangs up":    func(h turnHarness) { h.transport.EndAll() },
		"a newer one replaces it": func(h turnHarness) { _, _, _ = h.svc.Connect(context.Background(), h.caller, h.session, "offer") },
	} {
		t.Run(name, func(t *testing.T) {
			h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: packetsPerWord})
			c := h.connect(t)
			c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
			until(t, c, "done") // the reply's 0.8s of audio has only begun
			end(h)
			closeService(t, h.svc) // returns only once the turn has let go of the connection
			if n := len(c.Played()); n >= 4*packetsPerWord {
				t.Errorf("all %d packets played after the connection ended", n)
			}
			if saved := h.turns.All(); saved[0].Status != conversation.TurnAnswered {
				t.Errorf("the turn was saved %s, want answered: its reply had all arrived", saved[0].Status)
			}
		})
	}
}

// gatedCut is a turn store whose cut is saved only once release is closed,
// telling entered when it starts.
type gatedCut struct {
	conversation.Turns
	entered, release chan struct{}
}

func (g gatedCut) Cut(ctx context.Context, t conversation.Turn) error {
	close(g.entered)
	<-g.release
	return g.Turns.Cut(ctx, t)
}

// askedModel is the fake model, telling asked each time it is asked.
type askedModel struct {
	*modelfake.Model
	asked chan struct{}
}

func (m askedModel) Reply(ctx context.Context, r conversation.ModelRequest) <-chan conversation.ModelEvent {
	m.asked <- struct{}{}
	return m.Model.Reply(ctx, r)
}

func TestTheTurnAfterAnInterruptIsAskedWithOnlyThePartHeard(t *testing.T) {
	model := askedModel{&modelfake.Model{Pieces: replyPieces}, make(chan struct{}, 2)}
	gate := gatedCut{entered: make(chan struct{}), release: make(chan struct{})}
	h := newLoggingTurnHarness(t, model, ttsfake.TextToSpeech{PacketsPerWord: slowWords}, slog.New(slog.DiscardHandler),
		func(s conversation.Turns) conversation.Turns { gate.Turns = s; return gate })
	c := h.connect(t)
	c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
	<-model.asked
	until(t, c, "done")
	playedAtLeast(t, c, 2*slowWords+5)
	c.Say(turnMessage(uuid.NewV7(), "Hold on."))
	<-gate.entered
	// While the cut is held, a next turn that didn't wait for it asks the
	// model within milliseconds; one that waits never does.
	select {
	case <-model.asked:
		t.Error("the next turn asked the model before the cut was saved")
	case <-time.After(200 * time.Millisecond):
	}
	close(gate.release)
	until(t, c, "interrupted")
	until(t, c, "done")

	asked, _ := model.Requests()
	if got := asked[len(asked)-1].Conversation; len(got) != 2 || got[1].Text != "Tell me—" {
		t.Errorf("the next turn was asked with %q, want the reply only as far as heard: %q", got, "Tell me—")
	}
}

// brokenCut is a turn store that can't save a cut, as when the database is down.
type brokenCut struct{ conversation.Turns }

func (brokenCut) Cut(context.Context, conversation.Turn) error {
	return errors.New("the database is down")
}

func TestATurnIsStillTakenWhenSavingWhereTheOneBeforeWasCutFails(t *testing.T) {
	log := &logged{message: "save where an interrupted reply was cut"}
	model := &modelfake.Model{Pieces: replyPieces}
	h := newLoggingTurnHarness(t, model, ttsfake.TextToSpeech{PacketsPerWord: slowWords}, slog.New(log),
		func(s conversation.Turns) conversation.Turns { return brokenCut{s} })
	c := h.connect(t)
	c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
	until(t, c, "done")
	playedAtLeast(t, c, 2*slowWords+5)
	second := uuid.NewV7()
	c.Say(turnMessage(second, "Hold on."))

	got := until(t, c, "interrupted")
	if m := got[len(got)-1]; m.Heard == nil || *m.Heard != strings.Join(replyPieces, "") {
		t.Errorf("interrupted = %+v, want the whole reply heard, as the agent will remember it", m)
	}
	if got := until(t, c, "done"); got[len(got)-1].RequestID != second.String() {
		t.Errorf("then got %+v, want the second turn answered", got)
	}
	if _, ok := log.first(); !ok {
		t.Error("the failed save wasn't logged")
	}
	if asked, _ := model.Requests(); asked[1].Conversation[1].Text != strings.Join(replyPieces, "") {
		t.Errorf("the next turn was asked with %q, want the whole reply, as saved", asked[1].Conversation)
	}
}

// heldStart is a turn store whose first Start waits until release is closed,
// so the first turn is still being taken while others arrive.
type heldStart struct {
	conversation.Turns
	release chan struct{}
	once    *sync.Once
}

func (h heldStart) Start(ctx context.Context, t conversation.Turn, after int, deadline time.Duration) (int, error) {
	h.once.Do(func() { <-h.release })
	return h.Turns.Start(ctx, t, after, deadline)
}

func TestTurnsBeyondTheFewWaitingAreRefusedAsBusy(t *testing.T) {
	release := make(chan struct{})
	h := newLoggingTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1}, slog.New(slog.DiscardHandler),
		func(s conversation.Turns) conversation.Turns { return heldStart{s, release, &sync.Once{}} })
	c := h.connect(t)
	ids := []uuid.UUID{uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()}
	for _, id := range ids {
		c.Say(turnMessage(id, "Hello?"))
	}
	if m := next(t, c); m.Type != "refused" || m.Reason != "busy" || m.RequestID != ids[3].String() {
		t.Errorf("the fourth turn got %+v, want refused as busy: two already wait behind the first", m)
	}
	close(release)
	until(t, c, "spoken")
	saved := h.turns.All()
	if len(saved) != 3 || saved[0].RequestID != ids[0] || saved[1].RequestID != ids[1] || saved[2].RequestID != ids[2] {
		t.Errorf("saved %+v, want the first three turns in order and not the fourth", saved)
	}
}

func TestTheSameTurnSentAgainDoesntInterruptItself(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: packetsPerWord})
	c := h.connect(t)
	id := uuid.NewV7()
	c.Say(turnMessage(id, "Where should we start?"))
	until(t, c, "done") // its audio is still playing
	c.Say(turnMessage(id, "Where should we start?"))
	if m := next(t, c); m.Type != "spoken" || m.RequestID != id.String() {
		t.Errorf("got %+v, want the turn spoken to its end, not interrupted by its own retry", m)
	}
	if m := next(t, c); m.Type != "refused" || m.Reason != "repeated" {
		t.Errorf("the retry got %+v, want refused as repeated", m)
	}
}

// failingFirstStart is a turn store whose first Start fails, as when the
// database blinks, and whose later ones succeed.
type failingFirstStart struct {
	conversation.Turns
	failed *atomic.Bool
}

func (f failingFirstStart) Start(ctx context.Context, t conversation.Turn, after int, deadline time.Duration) (int, error) {
	if f.failed.CompareAndSwap(false, true) {
		return 0, errors.New("the database is down")
	}
	return f.Turns.Start(ctx, t, after, deadline)
}

func TestARetryAfterAFailureIsTheTurnTheNextOneInterrupts(t *testing.T) {
	h := newLoggingTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: slowWords}, slog.New(slog.DiscardHandler),
		func(s conversation.Turns) conversation.Turns { return failingFirstStart{s, &atomic.Bool{}} })
	c := h.connect(t)
	id, second := uuid.NewV7(), uuid.NewV7()
	c.Say(turnMessage(id, "Where should we start?"))
	if m := next(t, c); m.Type != "failed" {
		t.Fatalf("got %+v, want failed, so the page sends it again", m)
	}
	c.Say(turnMessage(id, "Where should we start?")) // the page's retry, answered this time
	until(t, c, "done")
	playedAtLeast(t, c, 2*slowWords+5)
	c.Say(turnMessage(second, "Hold on."))

	got := until(t, c, "interrupted")
	if m := got[len(got)-1]; m.RequestID != id.String() || *m.Heard != "Tell me" {
		t.Errorf("got %+v, want the retry's reply interrupted after %q", got, "Tell me")
	}
	if got := until(t, c, "done"); got[len(got)-1].RequestID != second.String() {
		t.Errorf("then got %+v, want the second turn answered", got)
	}
}

// heldPrompt is a turn store that, once armed, holds the first prompt read
// until release is closed, so that turn is still being taken while others
// arrive.
type heldPrompt struct {
	conversation.Turns
	armed   *atomic.Bool
	once    *sync.Once
	release chan struct{}
}

func (h heldPrompt) Finished(ctx context.Context, workspace, session uuid.UUID) ([]conversation.Turn, error) {
	if h.armed.Load() {
		h.once.Do(func() { <-h.release })
	}
	return h.Turns.Finished(ctx, workspace, session)
}

func TestResendsOfAnAnsweredTurnWaitOneAtATime(t *testing.T) {
	held := heldPrompt{armed: &atomic.Bool{}, once: &sync.Once{}, release: make(chan struct{})}
	log := &logged{message: "turn spoken"}
	h := newLoggingTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1}, slog.New(log),
		func(s conversation.Turns) conversation.Turns { held.Turns = s; return held })
	c := h.connect(t)
	id := uuid.NewV7()
	c.Say(turnMessage(id, "Where should we start?"))
	until(t, c, "spoken")
	// The page hears "spoken" just before the turn lets the session go, so a
	// resend in between would wait behind the answered turn itself. The turn
	// logs its stages only after letting go.
	waitLogged(t, log, 1, "the answered turn to let the session go")
	held.armed.Store(true)
	for range 20 {
		c.Say(turnMessage(id, "Where should we start?"))
	}
	// The first is held reading the conversation and one retry waits behind it.
	for i := range 18 {
		if m := next(t, c); m.Type != "refused" || m.Reason != "busy" {
			t.Fatalf("resend %d got %+v, want refused as busy: only one retry waits", i+3, m)
		}
	}
	close(held.release)
	for range 2 {
		if m := next(t, c); m.Type != "refused" || m.Reason != "repeated" {
			t.Errorf("got %+v, want the two taken refused as repeated", m)
		}
	}
}

func TestTheUserCanInterruptTimeAfterTime(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: slowWords})
	c := h.connect(t)
	ids := []uuid.UUID{uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()}
	c.Say(turnMessage(ids[0], "Where should we start?"))
	for i, id := range ids[1:] {
		until(t, c, "done")
		playedAtLeast(t, c, len(c.Played())+slowWords) // its reply is playing
		c.Say(turnMessage(id, "Hold on."))
		if got := until(t, c, "interrupted"); got[len(got)-1].RequestID != ids[i].String() {
			t.Fatalf("got %+v, want turn %d interrupted by turn %d", got, i+1, i+2)
		}
	}
	if got := until(t, c, "spoken"); got[len(got)-1].RequestID != ids[3].String() {
		t.Errorf("got %+v, want the fourth turn answered and spoken", got)
	}
}

func TestTwoRetriesLeaveRoomForTheUsersTurn(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: slowWords})
	c := h.connect(t)
	id, theirs := uuid.NewV7(), uuid.NewV7()
	c.Say(turnMessage(id, "Where should we start?"))
	until(t, c, "done") // its audio is playing
	c.Say(turnMessage(id, "Where should we start?"))
	c.Say(turnMessage(id, "Where should we start?"))
	c.Say(turnMessage(theirs, "Hold on."))

	if m := next(t, c); m.Type != "refused" || m.Reason != "busy" || m.RequestID != id.String() {
		t.Errorf("got %+v, want the second retry refused as busy: one already waits", m)
	}
	if got := until(t, c, "interrupted"); got[len(got)-1].RequestID != id.String() {
		t.Errorf("got %+v, want the reply playing interrupted by the user's turn", got)
	}
	if got := until(t, c, "done"); got[len(got)-1].RequestID != theirs.String() {
		t.Errorf("then got %+v, want the user's turn answered", got)
	}
}
