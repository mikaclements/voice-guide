package conversation_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	agentsfake "github.com/mikaclements/voice-guide/internal/agents/store/fake"
	"github.com/mikaclements/voice-guide/internal/conversation"
	modelfake "github.com/mikaclements/voice-guide/internal/conversation/model/fake"
	sessionsfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	itemsfake "github.com/mikaclements/voice-guide/internal/usercontext/store/fake"
)

// The limit on what a session loads about its user, in characters.
const contextLimit = 1000

// What user context holds about the user in these tests.
var (
	summary = usercontext.Item{ID: uuid.MustParse("01a1de8a-0000-7000-8000-0000000000d1"), Workspace: workspace, User: user, Kind: "session_summary", Content: "Deckhand, two seasons.", Agent: keeperID, Session: uuid.MustParse("01a1de8a-0000-7000-8000-0000000000e1")}
	profile = usercontext.Item{ID: uuid.MustParse("01a1de8a-0000-7000-8000-0000000000d2"), Workspace: workspace, User: user, Kind: "profile", Content: "Likes the night watch."}
)

// countingItems is user context's fake store, counting the reads that reach
// it and failing them while err is set.
type countingItems struct {
	*itemsfake.Items
	reads int
	err   error
	byID  int
}

func (c *countingItems) Current(ctx context.Context, ws, u uuid.UUID, kinds []string, agent uuid.UUID, limit int) ([]usercontext.Item, error) {
	c.reads++
	if c.err != nil {
		return nil, c.err
	}
	return c.Items.Current(ctx, ws, u, kinds, agent, limit)
}

func (c *countingItems) ByID(ctx context.Context, ws, u uuid.UUID, ids []uuid.UUID) ([]usercontext.Item, error) {
	c.byID++
	return c.Items.ByID(ctx, ws, u, ids)
}

// serviceOverFakes is the service over fake stores, with user context holding
// a summary of the user's last session and their profile, and two agents published in the workspace,
// the lighthouse keeper and the ferry captain.
type serviceOverFakes struct {
	svc      *conversation.Service
	agents   *agentsfake.Versions
	sessions *sessionsfake.Sessions
	turns    *sessionsfake.Turns
	model    *modelfake.Model
	items    *countingItems
	settings conversation.Settings
}

func newServiceOverFakes(t *testing.T) serviceOverFakes {
	t.Helper()
	return newServiceWithLimit(t, contextLimit)
}

func newServiceWithLimit(t *testing.T, limit int) serviceOverFakes {
	t.Helper()
	sessions := sessionsfake.New()
	h := serviceOverFakes{agents: agentsfake.New(), sessions: sessions, turns: sessionsfake.NewTurnsFor(sessions), model: &modelfake.Model{Pieces: replyPieces}, items: &countingItems{Items: itemsfake.New()}}
	h.items.Add(summary)
	h.items.Add(profile)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea."})
	h.publish(t, agents.Agent{ID: captainID, Name: "Ferry captain", Persona: "You know every crossing by heart."})
	h.settings = conversation.Settings{UserContextChars: limit, SessionContextChars: limit, CarriedChars: limit, ScreenInputChars: limit, WordsChars: limit, TurnDeadline: time.Minute, Model: "claude-test", SessionIdle: sessionIdle}
	h.svc = h.instance(h.model)
	return h
}

// instance returns another instance of the service over the same stores,
// answering turns with model: what a server added to serve the same users would hold.
func (h serviceOverFakes) instance(model conversation.Model) *conversation.Service {
	return conversation.NewService(agents.NewCatalog(h.agents), h.sessions, h.turns, model, usercontext.NewService(h.items), h.settings)
}

func (h serviceOverFakes) publish(t *testing.T, a agents.Agent) {
	t.Helper()
	if _, err := agents.Publish(context.Background(), h.agents, workspace, []agents.Agent{a}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

// start starts a session with agent from the product's server, for the user c
// names, who then takes turns in it as c.
func (h serviceOverFakes) start(t *testing.T, c access.Caller, agent, requestID uuid.UUID) (conversation.Session, bool) {
	t.Helper()
	s, isNew, err := h.startAs(c, conversation.StartRequest{Agent: agent, RequestID: requestID})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	return s, isNew
}

// startAs is StartSession from the product's server of c's workspace, for the
// user c names, whom the product has named by its own reference before.
func (h serviceOverFakes) startAs(c access.Caller, r conversation.StartRequest) (conversation.Session, bool, error) {
	r.User = "user-" + c.User.String()
	h.sessions.AddReference(c.Workspace, c.User, r.User)
	return h.svc.StartSession(context.Background(), access.Caller{Workspace: c.Workspace, Product: true}, r)
}

var caller = access.Caller{Workspace: workspace, User: user}

func TestASessionStartsOnTheAgentsNewestVersionForTheCaller(t *testing.T) {
	h := newServiceOverFakes(t)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You keep the lamp lit."})

	s, isNew := h.start(t, caller, keeperID, uuid.NewV7())
	if !isNew || s.Agent != keeperID || s.AgentVersion != 2 || s.Workspace != workspace || s.User != user {
		t.Errorf("StartSession = %+v, %v; want a new session on version 2 for the caller", s, isNew)
	}
}

func TestSessionIDsAreUUIDv7(t *testing.T) {
	s, _ := newServiceOverFakes(t).start(t, caller, keeperID, uuid.NewV7())
	if s.ID[6]>>4 != 7 {
		t.Errorf("session ID %v is not a UUIDv7", s.ID)
	}
}

func TestPressingStartTwiceMakesOneSession(t *testing.T) {
	h := newServiceOverFakes(t)
	request := uuid.NewV7()
	first, _ := h.start(t, caller, keeperID, request)

	again, isNew := h.start(t, caller, keeperID, request)
	if isNew || again.ID != first.ID || h.sessions.Count() != 1 {
		t.Errorf("second start = %+v, new %v, %d sessions; want the first session back", again, isNew, h.sessions.Count())
	}
}

func TestAgentsOutOfTheCallersReachAreRefusedAlike(t *testing.T) {
	h := newServiceOverFakes(t)
	tests := map[string]struct {
		caller access.Caller
		agent  uuid.UUID
	}{
		"an agent nobody published":  {caller, uuid.NewV7()},
		"another workspace's caller": {access.Caller{Workspace: uuid.NewV7(), User: user}, keeperID},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := h.startAs(tt.caller, conversation.StartRequest{Agent: tt.agent, RequestID: uuid.NewV7()})
			if !errors.Is(err, conversation.ErrNoSuchAgent) {
				t.Errorf("StartSession = %v, want ErrNoSuchAgent", err)
			}
		})
	}
	if h.sessions.Count() != 0 {
		t.Errorf("%d sessions started, want none", h.sessions.Count())
	}
}

func TestARunningSessionKeepsItsVersionAndTheNextGetsTheNewOne(t *testing.T) {
	h := newServiceOverFakes(t)
	running, _ := h.start(t, caller, keeperID, uuid.NewV7())
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You keep the lamp lit."})

	again, _ := h.start(t, caller, keeperID, uuid.NewV7())
	if running.AgentVersion != 1 || again.AgentVersion != 2 {
		t.Errorf("versions = %d then %d, want 1 kept by the running session and 2 for the next", running.AgentVersion, again.AgentVersion)
	}
}

func TestARunningSessionsTurnsSpeakAsTheVersionItStartedOn(t *testing.T) {
	h := newServiceOverFakes(t)
	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You keep the lamp lit."})

	h.takeTurn(t, turnIn(s.ID, "Is the lamp lit?"))
	if asked, _ := h.model.Requests(); len(asked) != 1 || asked[0].StableLayers[0] != "You answer every call from the sea." {
		t.Errorf("model asked %+v; want version 1's persona, the one the session started on", asked)
	}
}

func TestOneRequestIDFromTwoUsersMakesTwoSessions(t *testing.T) {
	h := newServiceOverFakes(t)
	request := uuid.NewV7()
	mine, _ := h.start(t, caller, keeperID, request)
	theirs, isNew := h.start(t, access.Caller{Workspace: workspace, User: uuid.NewV7()}, keeperID, request)
	if !isNew || theirs.ID == mine.ID {
		t.Errorf("another user's start with my request ID = %+v, new %v; want their own session", theirs, isNew)
	}
}

func TestReusingARequestIDForAnotherAgentIsRefused(t *testing.T) {
	h := newServiceOverFakes(t)
	request := uuid.NewV7()
	first, _ := h.start(t, caller, keeperID, request)

	if _, _, err := h.startAs(caller, conversation.StartRequest{Agent: captainID, RequestID: request}); !errors.Is(err, conversation.ErrRequestReused) {
		t.Errorf("StartSession with the request ID for another agent = %v, want ErrRequestReused", err)
	}
	if again, _ := h.start(t, caller, keeperID, request); again.ID != first.ID || h.sessions.Count() != 1 {
		t.Errorf("after the refusal: %+v and %d sessions, want only the first session", again, h.sessions.Count())
	}
}

func TestAStartLoadsWhatTheAgentsVersionReadsAndRecordsIt(t *testing.T) {
	h := newServiceOverFakes(t)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Reads: []string{"session_summary", "profile"}, Persona: "You read every log."})

	s, _ := h.start(t, caller, keeperID, uuid.NewV7())
	if !slices.Equal(s.ContextItems, []uuid.UUID{summary.ID, profile.ID}) || h.items.reads != 1 {
		t.Errorf("session's items = %v after %d reads, want the summary's and the profile's IDs in the version's order, from one read", s.ContextItems, h.items.reads)
	}
}

func TestAStartLoadsOnlyWhatFitsTheUserContextLimit(t *testing.T) {
	// The summary and the profile have 22 characters each, so only the first in the version's order fits.
	h := newServiceWithLimit(t, 30)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Reads: []string{"profile", "session_summary"}, Persona: "You read every log."})

	if s, _ := h.start(t, caller, keeperID, uuid.NewV7()); !slices.Equal(s.ContextItems, []uuid.UUID{profile.ID}) {
		t.Errorf("session's items = %v, want only the profile's ID", s.ContextItems)
	}
}

func TestARepeatStartKeepsWhatTheFirstStartLoaded(t *testing.T) {
	h := newServiceOverFakes(t)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Reads: []string{"session_summary"}, Persona: "You read every log."})
	request := uuid.NewV7()
	first, _ := h.start(t, caller, keeperID, request)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Reads: []string{"profile"}, Persona: "You read every log."})

	again, _ := h.start(t, caller, keeperID, request)
	if again.AgentVersion != first.AgentVersion || !slices.Equal(again.ContextItems, []uuid.UUID{summary.ID}) {
		t.Errorf("repeat = version %d with %v; want the first start's version %d with only the summary", again.AgentVersion, again.ContextItems, first.AgentVersion)
	}
	if h.items.reads != 1 {
		t.Errorf("user context read %d times, want once: a repeat doesn't load again", h.items.reads)
	}
}

func TestARepeatIsAnsweredWhileUserContextIsDown(t *testing.T) {
	h := newServiceOverFakes(t)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Reads: []string{"session_summary"}, Persona: "You read every log."})
	request := uuid.NewV7()
	first, _ := h.start(t, caller, keeperID, request)
	h.items.err = errors.New("user context unreachable")

	if again, isNew := h.start(t, caller, keeperID, request); isNew || again.ID != first.ID {
		t.Errorf("repeat = %+v, new %v; want the first session back, its reply having been lost", again, isNew)
	}
}

func TestAnAgentThatReadsNothingStartsWhileUserContextIsDown(t *testing.T) {
	h := newServiceOverFakes(t)
	h.items.err = errors.New("user context unreachable")

	if s, isNew := h.start(t, caller, captainID, uuid.NewV7()); !isNew || len(s.ContextItems) != 0 || h.items.reads != 0 {
		t.Errorf("start = %+v, new %v, %d reads; want a new session that never asked user context", s, isNew, h.items.reads)
	}
}

func TestAFailedLoadStartsNothingAndTheSameRequestCanBeTriedAgain(t *testing.T) {
	h := newServiceOverFakes(t)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Reads: []string{"session_summary"}, Persona: "You read every log."})
	h.items.err = errors.New("user context unreachable")
	request := uuid.NewV7()
	if _, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: request}); err == nil {
		t.Fatal("StartSession succeeded without the user's context")
	}
	if h.sessions.Count() != 0 {
		t.Fatalf("%d sessions saved after the failed load, want none", h.sessions.Count())
	}

	h.items.err = nil
	if _, isNew := h.start(t, caller, keeperID, request); !isNew {
		t.Error("the retry with the same request ID found an earlier session, want a new one")
	}
}

// keeperExpectingDocuments is the service with the lighthouse keeper expecting two documents from
// the product, named resume and page, within limit characters of session context.
func keeperExpectingDocuments(t *testing.T, limit int) serviceOverFakes {
	t.Helper()
	h := newServiceOverFakes(t)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", SessionContext: []string{"resume", "page"}, Persona: "You read every log."})
	h.svc = conversation.NewService(agents.NewCatalog(h.agents), h.sessions, h.turns, h.model, usercontext.NewService(h.items),
		conversation.Settings{UserContextChars: contextLimit, SessionContextChars: limit})
	return h
}

func block(name, content string) conversation.SessionContextBlock {
	return conversation.SessionContextBlock{Name: name, Content: content}
}

func TestTheSessionContextIsRecordedInOrder(t *testing.T) {
	h := keeperExpectingDocuments(t, 1000)
	sent := []conversation.SessionContextBlock{block("resume", "Deckhand, two seasons."), block("page", "Harbour charts")}

	s, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), SessionContext: sent})
	if err != nil || !slices.Equal(s.SessionContext, sent) {
		t.Errorf("StartSession = %+v, %v; want the session context recorded as sent", s.SessionContext, err)
	}
}

func TestSessionContextThatDoesntMatchTheAgentIsRefusedAndNothingSaved(t *testing.T) {
	resumeBlock, pageBlock := block("resume", "Deckhand, two seasons."), block("page", "Harbour charts")
	tests := map[string]struct {
		sent []conversation.SessionContextBlock
		want conversation.SessionContextMismatchError
	}{
		"one missing":    {[]conversation.SessionContextBlock{resumeBlock}, conversation.SessionContextMismatchError{Missing: []string{"page"}}},
		"one not wanted": {[]conversation.SessionContextBlock{resumeBlock, pageBlock, block("diary", "Saw a whale.")}, conversation.SessionContextMismatchError{Unknown: []string{"diary"}}},
		"one sent twice": {[]conversation.SessionContextBlock{resumeBlock, pageBlock, resumeBlock}, conversation.SessionContextMismatchError{Repeated: []string{"resume"}}},
		"one empty":      {[]conversation.SessionContextBlock{resumeBlock, block("page", "")}, conversation.SessionContextMismatchError{Empty: []string{"page"}}},
		// PostgreSQL can't store it, so it is refused here rather than failing every retry.
		"one with a NUL": {[]conversation.SessionContextBlock{resumeBlock, block("page", "Harbour\x00charts")}, conversation.SessionContextMismatchError{Unstorable: []string{"page"}}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			h := keeperExpectingDocuments(t, 1000)
			_, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), SessionContext: tt.sent})
			var got *conversation.SessionContextMismatchError
			if !errors.As(err, &got) || !reflect.DeepEqual(*got, tt.want) {
				t.Errorf("StartSession = %v, want a mismatch %+v", err, tt.want)
			}
			if h.sessions.Count() != 0 {
				t.Errorf("%d sessions saved, want none", h.sessions.Count())
			}
		})
	}
}

func TestSessionContextOverTheLimitIsRefusedCountingCharacters(t *testing.T) {
	// Twelve characters in all, 36 bytes in UTF-8: within a limit of 12, over one of 11.
	sent := []conversation.SessionContextBlock{block("resume", "夜の見張り番を二年"), block("page", "海図。")}
	for limit, refused := range map[int]bool{12: false, 11: true} {
		h := keeperExpectingDocuments(t, limit)
		_, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), SessionContext: sent})
		if refused != errors.Is(err, conversation.ErrSessionContextTooLarge) || (refused && h.sessions.Count() != 0) {
			t.Errorf("limit %d: StartSession = %v with %d sessions saved; refused should be %v", limit, err, h.sessions.Count(), refused)
		}
	}
}

func TestAnAgentThatExpectsNothingTakesNoSessionContext(t *testing.T) {
	h := newServiceOverFakes(t)
	if s, isNew := h.start(t, caller, captainID, uuid.NewV7()); !isNew || len(s.SessionContext) != 0 {
		t.Errorf("start without session context = %+v, new %v; want a new session with none", s, isNew)
	}
	_, _, err := h.startAs(caller, conversation.StartRequest{Agent: captainID, RequestID: uuid.NewV7(), SessionContext: []conversation.SessionContextBlock{block("resume", "Deckhand.")}})
	var mismatch *conversation.SessionContextMismatchError
	if !errors.As(err, &mismatch) || !slices.Equal(mismatch.Unknown, []string{"resume"}) {
		t.Errorf("start with a block the agent doesn't expect = %v, want it refused as unknown", err)
	}
}

func TestARepeatWithTheSameSessionContextGetsTheFirstSession(t *testing.T) {
	h := keeperExpectingDocuments(t, 1000)
	request := uuid.NewV7()
	first := []conversation.SessionContextBlock{block("resume", "Deckhand, two seasons."), block("page", "Harbour charts")}
	if _, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: request, SessionContext: first}); err != nil {
		t.Fatalf("first start: %v", err)
	}

	same := []conversation.SessionContextBlock{block("resume", "Deckhand, two seasons."), block("page", "Harbour charts")}
	again, isNew, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: request, SessionContext: same})
	if err != nil || isNew || !slices.Equal(again.SessionContext, first) {
		t.Errorf("repeat = %+v, new %v, %v; want the first session with its first session context", again.SessionContext, isNew, err)
	}
}

func TestARepeatWithDifferentSessionContextIsRefused(t *testing.T) {
	first := []conversation.SessionContextBlock{block("resume", "Deckhand, two seasons."), block("page", "Harbour charts")}
	tests := map[string][]conversation.SessionContextBlock{
		"another content": {block("resume", "Bosun."), block("page", "Harbour charts")},
		"another name":    {block("resume", "Deckhand, two seasons."), block("chart", "Harbour charts")},
		"another order":   {block("page", "Harbour charts"), block("resume", "Deckhand, two seasons.")},
		"one left out":    {block("resume", "Deckhand, two seasons.")},
		"none":            nil,
	}
	for name, repeat := range tests {
		t.Run(name, func(t *testing.T) {
			h := keeperExpectingDocuments(t, 1000)
			request := uuid.NewV7()
			started, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: request, SessionContext: first})
			if err != nil {
				t.Fatalf("first start: %v", err)
			}

			_, _, err = h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: request, SessionContext: repeat})
			if !errors.Is(err, conversation.ErrRequestReused) || !strings.Contains(err.Error(), "different session context") {
				t.Errorf("repeat = %v, want ErrRequestReused naming different session context", err)
			}
			if again, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: request, SessionContext: first}); err != nil || again.ID != started.ID || h.sessions.Count() != 1 {
				t.Errorf("after the refusal: %+v, %v and %d sessions; want only the first session", again, err, h.sessions.Count())
			}
		})
	}
}

// staleLookup is the fake store of sessions with a lookup that finds nothing,
// as when another start with the same request ID has yet to commit.
type staleLookup struct{ *sessionsfake.Sessions }

func (staleLookup) ForRequest(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (conversation.Session, bool, error) {
	return conversation.Session{}, false, nil
}

func TestAStartThatLosesARaceIsAnsweredLikeARepeat(t *testing.T) {
	sent := []conversation.SessionContextBlock{block("resume", "Deckhand, two seasons."), block("page", "Harbour charts")}
	other := []conversation.SessionContextBlock{block("resume", "Bosun."), block("page", "Harbour charts")}
	tests := map[string]struct {
		loser conversation.StartRequest
		want  error
	}{
		"the same request":          {conversation.StartRequest{Agent: keeperID, SessionContext: sent}, nil},
		"another agent":             {conversation.StartRequest{Agent: captainID}, conversation.ErrRequestReused},
		"different session context": {conversation.StartRequest{Agent: keeperID, SessionContext: other}, conversation.ErrRequestReused},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			h := keeperExpectingDocuments(t, 1000)
			request := uuid.NewV7()
			winner, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: request, SessionContext: sent})
			if err != nil {
				t.Fatalf("winning start: %v", err)
			}
			// The loser runs on another instance, whose lookup misses the winner's session.
			h.svc = conversation.NewService(agents.NewCatalog(h.agents), staleLookup{h.sessions}, h.turns, h.model, usercontext.NewService(h.items),
				conversation.Settings{UserContextChars: contextLimit, SessionContextChars: 1000})

			tt.loser.RequestID = request
			got, isNew, err := h.startAs(caller, tt.loser)
			if !errors.Is(err, tt.want) || (tt.want == nil && (isNew || got.ID != winner.ID)) {
				t.Errorf("losing start = %+v, new %v, %v; want %v, or the winner's session when nil", got, isNew, err, tt.want)
			}
		})
	}
}

func TestAMismatchNamesAtMostFiveNamesEachCutShort(t *testing.T) {
	h := keeperExpectingDocuments(t, 1000)
	long := strings.Repeat("x", 50)
	sent := []conversation.SessionContextBlock{block("resume", "Deckhand."), block("page", "Charts."), block(long, "a")}
	for _, name := range []string{"b", "c", "d", "e", "f", "g"} {
		sent = append(sent, block(name, "a"))
	}
	_, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), SessionContext: sent})
	msg := fmt.Sprint(err)
	if !strings.Contains(msg, strings.Repeat("x", 40)+"…") || strings.Contains(msg, long) || !strings.Contains(msg, "and 2 more") {
		t.Errorf("message = %q, want the long name cut at 40 characters and only five names listed", msg)
	}
}

// rewritingSessions is a store that saves session context slightly changed, as
// a database may (PostgreSQL's JSON type turns invalid UTF-8 into U+FFFD).
type rewritingSessions struct{ *sessionsfake.Sessions }

func (r rewritingSessions) Start(ctx context.Context, s conversation.Session, requestID uuid.UUID, reference string) (conversation.Session, bool, error) {
	for i := range s.SessionContext {
		s.SessionContext[i].Content += "�"
	}
	return r.Sessions.Start(ctx, s, requestID, reference)
}

func TestANewSessionIsNotComparedWithItselfAsStored(t *testing.T) {
	h := keeperExpectingDocuments(t, 1000)
	h.svc = conversation.NewService(agents.NewCatalog(h.agents), rewritingSessions{h.sessions}, h.turns, h.model, usercontext.NewService(h.items),
		conversation.Settings{UserContextChars: contextLimit, SessionContextChars: 1000})
	sent := []conversation.SessionContextBlock{block("resume", "Deckhand."), block("page", "Charts.")}

	if _, isNew, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), SessionContext: sent}); err != nil || !isNew {
		t.Errorf("first start = new %v, %v; want a new session, not a refusal of its own request", isNew, err)
	}
}

// productCaller is a product's server calling with its key: the workspace, and no user until the
// start names one.
var productCaller = access.Caller{Workspace: workspace, Product: true}

func (h serviceOverFakes) startFor(t *testing.T, reference string, requestID uuid.UUID) (conversation.Session, bool) {
	t.Helper()
	s, isNew, err := h.svc.StartSession(context.Background(), productCaller, conversation.StartRequest{Agent: keeperID, RequestID: requestID, User: reference})
	if err != nil {
		t.Fatalf("StartSession for %q: %v", reference, err)
	}
	return s, isNew
}

func TestAProductNamesEachUserByItsOwnReference(t *testing.T) {
	h := newServiceOverFakes(t)
	first, _ := h.startFor(t, "aiko-42", uuid.NewV7())
	again, _ := h.startFor(t, "aiko-42", uuid.NewV7())
	other, _ := h.startFor(t, "ben-7", uuid.NewV7())
	if first.User == uuid.Nil() || again.User != first.User || other.User == first.User {
		t.Errorf("users %v, %v, %v; want one user per reference, the same each time", first.User, again.User, other.User)
	}
}

func TestAProductPressingStartTwiceMakesOneSession(t *testing.T) {
	h := newServiceOverFakes(t)
	request := uuid.NewV7()
	first, _ := h.startFor(t, "aiko-42", request)
	again, isNew := h.startFor(t, "aiko-42", request)
	if isNew || again.ID != first.ID {
		t.Errorf("repeat = %v, new %v; want the first session %v", again.ID, isNew, first.ID)
	}
}

func TestARefusedStartForANewReferenceSavesNoUser(t *testing.T) {
	h := newServiceOverFakes(t)
	undeclared := []conversation.SessionContextBlock{block("resume", "Deckhand.")}
	_, _, err := h.svc.StartSession(context.Background(), productCaller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), User: "aiko-42", SessionContext: undeclared})
	var mismatch *conversation.SessionContextMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("StartSession err = %v; want a session context mismatch", err)
	}
	if user, found, _ := h.sessions.UserByReference(context.Background(), workspace, "aiko-42"); found {
		t.Errorf("the refused start saved user %v for the reference", user)
	}
}

// failingReferences is a store of sessions whose user references can't be read.
type failingReferences struct{ *sessionsfake.Sessions }

func (failingReferences) UserByReference(context.Context, uuid.UUID, string) (uuid.UUID, bool, error) {
	return uuid.Nil(), false, errors.New("connection refused")
}

func TestAStartWhoseUserCantBeLookedUpIsUnavailable(t *testing.T) {
	h := newServiceOverFakes(t)
	h.svc = conversation.NewService(agents.NewCatalog(h.agents), failingReferences{h.sessions}, h.turns, h.model, usercontext.NewService(h.items), h.settings)
	_, _, err := h.svc.StartSession(context.Background(), productCaller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), User: "aiko-42"})
	if !errors.Is(err, conversation.ErrUserUnavailable) {
		t.Errorf("StartSession err = %v; want ErrUserUnavailable", err)
	}
}

func TestAUserReferenceIsAnIDOfAtMost200PrintableCharactersWithNoSpaceAtEitherEnd(t *testing.T) {
	h := newServiceOverFakes(t)
	for name, reference := range map[string]string{
		"missing":              "",
		"only spaces":          "   ",
		"a leading space":      " user-8f3a",
		"a trailing space":     "user-8f3a ",
		"a trailing tab":       "user-8f3a\t",
		"201 characters":       strings.Repeat("é", 201),
		"a line break":         "aiko\n42",
		"a control":            "aiko\x0042",
		"not UTF-8":            "aiko\xff",
		"a line separator":     "aiko\u202842",
		"a paragraph break":    "aiko\u202942",
		"a direction override": "aiko\u202e24",
		"a zero-width space":   "aiko\u200b42",
		"a byte order mark":    "\ufeffaiko-42",
	} {
		_, _, err := h.svc.StartSession(context.Background(), productCaller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), User: reference})
		if !errors.Is(err, conversation.ErrUserReference) {
			t.Errorf("%s: StartSession err = %v; want ErrUserReference", name, err)
		}
	}
	for _, reference := range []string{strings.Repeat("é", 200), "user-8f3a", "42", "org:7/member 9", "顧客-0042"} {
		if _, isNew := h.startFor(t, reference, uuid.NewV7()); !isNew {
			t.Errorf("%q was refused", reference)
		}
	}
}
