package http_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/conversation"
	modelfake "github.com/mikaclements/voice-guide/internal/conversation/model/fake"
	sessionsfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/productkey"
	keysfake "github.com/mikaclements/voice-guide/internal/productkey/store/fake"
	"github.com/mikaclements/voice-guide/internal/ticket"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	itemsfake "github.com/mikaclements/voice-guide/internal/usercontext/store/fake"
)

type turnFixture struct {
	fixture
	turns   *sessionsfake.Turns
	session string
	// owner is the caller the session's ticket names, and own that ticket.
	owner access.Caller
	own   string
}

// withSession serves the API over a model and a turn deadline, with a
// session started with the lighthouse keeper.
func withSession(t *testing.T, model conversation.Model, deadline time.Duration) turnFixture {
	t.Helper()
	turns := sessionsfake.NewTurns()
	f := withSessionOver(t, turns, model, deadline)
	f.turns = turns
	return f
}

// withSessionOver is withSession over a store of turns of the test's own.
func withSessionOver(t *testing.T, turns conversation.Turns, model conversation.Model, deadline time.Duration) turnFixture {
	t.Helper()
	key, err := ticket.NewKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	svc := conversation.NewService(agents.NewCatalog(newFixtureStore(t)), sessionsfake.New(), turns, model, usercontext.NewService(itemsfake.New()),
		conversation.Settings{UserContextChars: 1000, SessionContextChars: 1000, ScreenInputChars: 1000, WordsChars: 20, TurnDeadline: deadline, Model: "claude-test"})
	keys := productkey.NewKeys(keysfake.New())
	f := fixture{handler: handlerFor(svc, key, keys, slog.New(slog.NewTextHandler(io.Discard, nil))), key: key, keys: keys}
	started, owner := f.startFor(t, "aiko-42")
	return turnFixture{fixture: f, session: started.ID, owner: owner, own: started.Ticket}
}

// inWorkspace and forUser return the caller changed in one respect, to ask for
// what a ticket naming the session's workspace and user doesn't reach.
func inWorkspace(c access.Caller, ws uuid.UUID) access.Caller { c.Workspace = ws; return c }
func forUser(c access.Caller, u uuid.UUID) access.Caller      { c.User = u; return c }

func pieces(p ...string) *modelfake.Model { return &modelfake.Model{Pieces: p} }

func turnBody(words string) string {
	return `{"request_id":"` + uuid.NewV7().String() + `","words":"` + words + `"}`
}

func (f turnFixture) turn(t *testing.T, c access.Caller, body string) *httptest.ResponseRecorder {
	return f.send("POST", "/v1/sessions/"+f.session+"/turns", f.ticketFor(t, c, f.session), body)
}

// events splits a stream of server-sent events into lines of name and data.
func events(body string) []string {
	var all []string
	for _, e := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		all = append(all, strings.ReplaceAll(strings.TrimPrefix(e, "event: "), "\ndata: ", " "))
	}
	return all
}

// flushCounter is a response recorder that counts its flushes.
type flushCounter struct {
	*httptest.ResponseRecorder
	flushes int
}

func (f *flushCounter) Flush() { f.flushes++; f.ResponseRecorder.Flush() }

func TestATurnStreamsTheReplyAsEventsEachFlushedThenDone(t *testing.T) {
	f := withSession(t, pieces("Bearing", " 040.\n\n", "Mind the rocks."), time.Minute)
	req := httptest.NewRequest("POST", "/v1/sessions/"+f.session+"/turns", strings.NewReader(
		`{"request_id":"`+uuid.NewV7().String()+`","words":"Where is the ferry?","screen_input":[{"name":"chart","content":"Rocks to the north."}]}`))
	req.Header.Set("Authorization", "Bearer "+f.own)
	rec := &flushCounter{ResponseRecorder: httptest.NewRecorder()}
	f.handler.ServeHTTP(rec, req)

	saved := f.turns.All()
	if rec.Code != nethttp.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" || len(saved) != 1 {
		t.Fatalf("status %d, %s, %d turns saved; want 200, an event stream and one turn: %s", rec.Code, rec.Header().Get("Content-Type"), len(saved), rec.Body)
	}
	if rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Error("the stream doesn't ask a proxy in front not to buffer it")
	}
	want := []string{`text {"text":"Bearing"}`, `text {"text":" 040.\n\n"}`, `text {"text":"Mind the rocks."}`, `done {"turn_id":"` + saved[0].ID.String() + `","seq":1,"model":"fake/claude-test"}`}
	if got := events(rec.Body.String()); !reflect.DeepEqual(got, want) || rec.flushes != len(want) {
		t.Errorf("events = %q with %d flushes, want %q, each flushed", got, rec.flushes, want)
	}
	if blocks := saved[0].ScreenInput; len(blocks) != 1 || blocks[0] != (conversation.ScreenInputBlock{Name: "chart", Content: "Rocks to the north."}) {
		t.Errorf("saved screen input %+v, want the chart", blocks)
	}
}

func TestSessionsOutOfReachAnswer404Alike(t *testing.T) {
	f := withSession(t, pieces("Aye."), time.Minute)
	tests := map[string]struct {
		caller  access.Caller
		session string
	}{
		"another workspace's caller":   {inWorkspace(f.owner, uuid.NewV7()), f.session},
		"another user":                 {forUser(f.owner, uuid.NewV7()), f.session},
		"a session that doesn't exist": {f.owner, uuid.NewV7().String()},
	}
	var bodies []string
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := f.send("POST", "/v1/sessions/"+tt.session+"/turns", f.ticketFor(t, tt.caller, tt.session), turnBody("Is the lamp lit?"))
			bodies = append(bodies, rec.Body.String())
			assertProblem(t, rec, nethttp.StatusNotFound)
		})
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Errorf("404 bodies differ: %q and %q; a caller could tell the cases apart", bodies[0], b)
		}
	}
}

func TestATurnWhileAnotherIsInFlightIsA409(t *testing.T) {
	f := withSession(t, pieces("Aye."), time.Minute)
	session := uuid.MustParse(f.session)
	if _, err := f.turns.Start(context.Background(), conversation.Turn{ID: uuid.NewV7(), Workspace: workspace, User: user, Session: session}, 0, time.Minute); err != nil {
		t.Fatalf("Start: %v", err)
	}
	assertProblem(t, f.turn(t, f.owner, turnBody("And the fog?")), nethttp.StatusConflict)
}

func TestAMalformedTurnIsA400(t *testing.T) {
	f := withSession(t, pieces("Aye."), time.Minute)
	request := uuid.NewV7().String()
	for name, body := range map[string]string{
		"not JSON":                             "lamp please",
		"no request ID":                        `{"words":"Is the lamp lit?"}`,
		"a nil request ID":                     `{"request_id":"` + uuid.Nil().String() + `","words":"Is the lamp lit?"}`,
		"no words":                             `{"request_id":"` + request + `"}`,
		"a screen input name a tag can't hold": `{"request_id":"` + request + `","words":"Where?","screen_input":[{"name":"Tide table","content":"Six."}]}`,
		"screen input not a list":              `{"request_id":"` + request + `","words":"Where?","screen_input":"chart"}`,
		"words that aren't text":               `{"request_id":"` + request + `","words":40}`,
	} {
		t.Run(name, func(t *testing.T) { assertProblem(t, f.turn(t, f.owner, body), nethttp.StatusBadRequest) })
	}
	if saved := f.turns.All(); len(saved) != 0 {
		t.Errorf("saved %+v, want nothing", saved)
	}
}

func TestWordsWithANulAreA400AndWordsOverTheLimitA413(t *testing.T) {
	f := withSession(t, pieces("Aye."), time.Minute)
	assertProblem(t, f.turn(t, f.owner, `{"request_id":"`+uuid.NewV7().String()+`","words":"Where\u0000?"}`), nethttp.StatusBadRequest)
	assertProblem(t, f.turn(t, f.owner, turnBody(strings.Repeat("海", 21))), nethttp.StatusRequestEntityTooLarge)
	for _, body := range []string{
		turnBody(strings.Repeat("海", 20)),
		`{"request_id":"` + uuid.NewV7().String() + `","screen_input":[{"name":"chart","content":"Rocks."}]}`,
	} {
		if rec := f.turn(t, f.owner, body); rec.Code != nethttp.StatusOK {
			t.Errorf("%s: status %d, want 200: %s", body, rec.Code, rec.Body)
		}
	}
}

func TestScreenInputOverItsLimitAndABodyOverTheByteLimitAreA413(t *testing.T) {
	f := withSession(t, pieces("Aye."), time.Minute)
	for name, body := range map[string]string{
		"screen input over its limit": `{"request_id":"` + uuid.NewV7().String() + `","words":"Where?","screen_input":[{"name":"chart","content":"` + strings.Repeat("海", 1001) + `"}]}`,
		// Over the fixture's body limit, about 78 KB, in a field nothing else
		// checks, so only the byte limit can refuse it.
		"a body over the byte limit": `{"request_id":"` + uuid.NewV7().String() + `","words":"Where?","padding":"` + strings.Repeat("a", 80_000) + `"}`,
	} {
		t.Run(name, func(t *testing.T) { assertProblem(t, f.turn(t, f.owner, body), nethttp.StatusRequestEntityTooLarge) })
	}
	if saved := f.turns.All(); len(saved) != 0 {
		t.Errorf("saved %+v, want nothing", saved)
	}
}

// A repeat is told apart from a turn sent while another is in flight, which
// the client may send again once that turn ends.
func TestARepeatedTurnIsA409OfItsOwnType(t *testing.T) {
	f := withSession(t, pieces("Aye."), time.Minute)
	body := turnBody("Is the lamp lit?")
	f.turn(t, f.owner, body)
	if p := assertProblemType(t, f.turn(t, f.owner, body), nethttp.StatusConflict, "/v1/problems/turn-repeated"); p.Detail == "" {
		t.Error("the problem doesn't say what to do")
	}
}

// The lighthouse keeper waits for the user, so its session takes no opening.
func TestAnOpeningTheSessionDoesntTakeIsA409OfItsOwnTypeAndOneWithWordsA400(t *testing.T) {
	f := withSession(t, pieces("Aye."), time.Minute)
	opening := `{"request_id":"` + uuid.NewV7().String() + `","opening":true}`
	assertProblemType(t, f.turn(t, f.owner, opening), nethttp.StatusConflict, "/v1/problems/opening-refused")
	assertProblem(t, f.turn(t, f.owner, `{"request_id":"`+uuid.NewV7().String()+`","opening":true,"words":"Hello?"}`), nethttp.StatusBadRequest)
}

// stalledModel sends its text, then nothing more until the reply is stopped.
type stalledModel struct{ text string }

func (m stalledModel) Reply(ctx context.Context, _ conversation.ModelRequest) <-chan conversation.ModelEvent {
	events := make(chan conversation.ModelEvent, 1)
	events <- conversation.ModelEvent{Text: m.text}
	go func() {
		<-ctx.Done()
		close(events)
	}()
	return events
}

func TestACallerThatDisconnectsMidReplyLeavesTheTurnSavedWithTheTextSent(t *testing.T) {
	f := withSession(t, stalledModel{"Bearing"}, time.Minute)
	srv := httptest.NewServer(f.handler)
	req, _ := nethttp.NewRequest("POST", srv.URL+"/v1/sessions/"+f.session+"/turns", strings.NewReader(turnBody("Where is the ferry?")))
	req.Header.Set("Authorization", "Bearer "+f.own)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	first := make([]byte, len("event: text\ndata: {\"text\":\"Bearing\"}\n\n"))
	_, err = io.ReadFull(resp.Body, first)
	_ = resp.Body.Close()
	srv.Close() // waits for the handler to return
	if saved := f.turns.All(); err != nil || len(saved) != 1 || saved[0].Status != conversation.TurnLost || saved[0].Reply != "Bearing" {
		t.Errorf("read %q (%v); saved %+v; want the turn saved lost with the text sent so far", first, err, saved)
	}
}

func TestAReplyThatFailsAfterItsFirstTextEndsWithLost(t *testing.T) {
	model := pieces("Bearing")
	model.Fail = "overloaded"
	f := withSession(t, model, time.Minute)
	rec := f.turn(t, f.owner, turnBody("Where is the ferry?"))
	saved := f.turns.All()
	want := []string{`text {"text":"Bearing"}`, `lost {"turn_id":"` + saved[0].ID.String() + `","seq":1,"model":"claude-test"}`}
	if got := events(rec.Body.String()); rec.Code != nethttp.StatusOK || !reflect.DeepEqual(got, want) {
		t.Errorf("status %d, events %q; want 200 and %q", rec.Code, got, want)
	}
}

// The turn is saved lost with the user's words, so the stream starts to say so
// rather than refusing the turn.
func TestAReplyThatFailsBeforeAnyTextEndsWithLostNotAProblem(t *testing.T) {
	f := withSession(t, &modelfake.Model{Fail: "overloaded"}, time.Minute)
	rec := f.turn(t, f.owner, turnBody("Where is the ferry?"))
	saved := f.turns.All()
	if len(saved) != 1 || saved[0].Status != conversation.TurnLost {
		t.Fatalf("saved %+v, want one turn, lost", saved)
	}
	want := []string{`lost {"turn_id":"` + saved[0].ID.String() + `","seq":1,"model":"claude-test"}`}
	if got := events(rec.Body.String()); rec.Code != nethttp.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" || !reflect.DeepEqual(got, want) {
		t.Errorf("status %d, %s, events %q; want 200, an event stream and %q", rec.Code, rec.Header().Get("Content-Type"), got, want)
	}
}

// failingFinish is the fake store of turns, failing to save how a turn ended.
type failingFinish struct{ *sessionsfake.Turns }

func (failingFinish) Finish(context.Context, conversation.Turn) error { return errors.New("disk full") }

// Only a turn saved as ended is named, so this lost names none.
func TestASaveThatFailsOnceTheReplyStreamedEndsWithLost(t *testing.T) {
	f := withSessionOver(t, failingFinish{sessionsfake.NewTurns()}, pieces("Bearing"), time.Minute)
	rec := f.turn(t, f.owner, turnBody("Where is the ferry?"))
	want := []string{`text {"text":"Bearing"}`, `lost {}`}
	if got := events(rec.Body.String()); rec.Code != nethttp.StatusOK || !reflect.DeepEqual(got, want) {
		t.Errorf("status %d, events %q; want 200 and %q", rec.Code, got, want)
	}
}

// deadlineModel answers at once, recording its context's deadline.
type deadlineModel struct{ deadline time.Time }

func (m *deadlineModel) Reply(ctx context.Context, _ conversation.ModelRequest) <-chan conversation.ModelEvent {
	m.deadline, _ = ctx.Deadline()
	events := make(chan conversation.ModelEvent, 1)
	events <- conversation.ModelEvent{Done: true}
	close(events)
	return events
}

func TestATurnRunsWithinTheTurnDeadlineNotTheShorterOneOfOtherRoutes(t *testing.T) {
	model := &deadlineModel{}
	f := withSession(t, model, time.Hour)
	f.turn(t, f.owner, turnBody("Is the lamp lit?"))
	if left := time.Until(model.deadline); left < 59*time.Minute || left > time.Hour {
		t.Errorf("the turn had %v left, want the hour of the turn deadline", left)
	}
}

// The server's own timeouts are much shorter than the reply, which streams
// for longer than both.
func TestATurnStreamsPastTheServersOwnTimeouts(t *testing.T) {
	model := pieces("Bearing", " 040.", " Mind", " the rocks.")
	model.Pace = 40 * time.Millisecond
	f := withSession(t, model, time.Minute)
	srv := httptest.NewUnstartedServer(f.handler)
	srv.Config.ReadTimeout, srv.Config.WriteTimeout = 50*time.Millisecond, 50*time.Millisecond
	srv.Start()
	defer srv.Close()

	req, _ := nethttp.NewRequest("POST", srv.URL+"/v1/sessions/"+f.session+"/turns", strings.NewReader(turnBody("Where is the ferry?")))
	req.Header.Set("Authorization", "Bearer "+f.own)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if got := events(string(body)); err != nil || len(got) != 5 || !strings.HasPrefix(got[4], "done ") {
		t.Errorf("events = %q (%v), want four texts and done", got, err)
	}
}

// slowStages holds back recording stage times until released, or fails it.
type slowStages struct {
	*sessionsfake.Turns
	release chan struct{}
	fail    error
}

func (s slowStages) RecordStages(ctx context.Context, t conversation.Turn) error {
	if s.fail != nil {
		return s.fail
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.Turns.RecordStages(ctx, t)
}

func TestTheReplyReachesTheCallerBeforeItsStageTimesAreWritten(t *testing.T) {
	stages := slowStages{Turns: sessionsfake.NewTurns(), release: make(chan struct{})}
	f := withSessionOver(t, stages, pieces("Bearing", " 040."), time.Minute)
	srv := httptest.NewServer(f.handler)
	defer srv.Close()
	req, _ := nethttp.NewRequest("POST", srv.URL+"/v1/sessions/"+f.session+"/turns", strings.NewReader(turnBody("Where is the ferry?")))
	req.Header.Set("Authorization", "Bearer "+f.own)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The write is held back until released, and gives up after 2s, so done arriving well
	// within that shows it was sent before the write; the fake model answers at once.
	sent := time.Now()
	var read strings.Builder
	buf := make([]byte, 256)
	for {
		n, err := resp.Body.Read(buf)
		read.Write(buf[:n])
		if strings.Contains(read.String(), "event: done") {
			break
		}
		if err != nil {
			t.Fatalf("read %q, then %v; want done", read.String(), err)
		}
	}
	if took := time.Since(sent); took > time.Second {
		t.Errorf("done arrived after %v, want it while the stage times are still held back", took)
	}
	close(stages.release)
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Errorf("the rest of the stream: %v", err)
	}
	saved := stages.All()
	if len(saved) != 1 || len(stages.Stages(saved[0].ID)) == 0 {
		t.Errorf("saved %+v; want the turn with its stage times once released", saved)
	}
}

func TestAStageTimeWriteThatFailsLeavesTheReplyAsItWas(t *testing.T) {
	f := withSessionOver(t, slowStages{Turns: sessionsfake.NewTurns(), fail: errors.New("disk full")}, pieces("Bearing", " 040."), time.Minute)
	rec := f.turn(t, f.owner, turnBody("Where is the ferry?"))
	if got := events(rec.Body.String()); rec.Code != nethttp.StatusOK || len(got) == 0 || !strings.HasPrefix(got[len(got)-1], "done ") {
		t.Errorf("status %d, events %q; want the reply ending with done", rec.Code, got)
	}
}
