package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/agents/store/fake"
	"github.com/mikaclements/voice-guide/internal/api"
	"github.com/mikaclements/voice-guide/internal/conversation"
	conversationhttp "github.com/mikaclements/voice-guide/internal/conversation/http"
	sessionsfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/productkey"
	keysfake "github.com/mikaclements/voice-guide/internal/productkey/store/fake"
	"github.com/mikaclements/voice-guide/internal/ticket"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	itemsfake "github.com/mikaclements/voice-guide/internal/usercontext/store/fake"
)

var (
	now       = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	workspace = uuid.MustParse("01a1de8a-0000-7000-8000-000000000001")
	user      = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a1")
	keeperID  = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000b1")
	captainID = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000b2")
)

type fixture struct {
	handler nethttp.Handler
	key     ticket.Key
	keys    *productkey.Keys
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	return fixtureOver(t, newFixtureStore(t))
}

// newFixtureStore returns a store with two agents published in the workspace,
// the lighthouse keeper and the ferry captain.
func newFixtureStore(t *testing.T) agents.Versions {
	t.Helper()
	store := fake.New()
	if _, err := agents.Publish(context.Background(), store, workspace, []agents.Agent{
		{ID: keeperID, Name: "Lighthouse keeper", Description: "Talks ships through the fog.", Reads: []string{"profile"}, Persona: "secret persona"},
		{ID: captainID, Name: "Ferry captain", Persona: "secret persona"},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return store
}

// fixtureOver returns the API over the given store of agent versions.
func fixtureOver(t *testing.T, store agents.Versions) fixture {
	t.Helper()
	return fixtureOverStores(t, store, sessionsfake.New(), itemsfake.New())
}

// fixtureOverStores returns the API over the given stores of agent versions, sessions and context items.
func fixtureOverStores(t *testing.T, store agents.Versions, sessions conversation.Sessions, items usercontext.Items) fixture {
	t.Helper()
	key, err := ticket.NewKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	keys := productkey.NewKeys(keysfake.New())
	return fixture{
		handler: handlerFor(conversation.NewService(agents.NewCatalog(store), sessions, nil, nil, usercontext.NewService(items), conversation.Settings{UserContextChars: 1000, SessionContextChars: 1000}), key, keys, log),
		key:     key,
		keys:    keys,
	}
}

// handlerFor serves conversation's routes on the API's router, taking the
// product keys in keys.
func handlerFor(svc *conversation.Service, key ticket.Key, keys *productkey.Keys, log *slog.Logger) nethttp.Handler {
	rt := api.NewRouter(key, keys, func() time.Time { return now }, log)
	conversationhttp.Register(rt, svc)
	return rt
}

// productKey returns a working key for the workspace.
func (f fixture) productKey(t *testing.T) string {
	t.Helper()
	return f.productKeyFor(t, workspace)
}

// productKeyFor returns a working key for the given workspace.
func (f fixture) productKeyFor(t *testing.T, ws uuid.UUID) string {
	t.Helper()
	s, _, err := f.keys.Create(context.Background(), ws, access.RunSessions)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return s.Reveal()
}

// ticketFor returns a ticket naming the caller in the session, as a start
// hands one back.
func (f fixture) ticketFor(t *testing.T, c access.Caller, session string) string {
	t.Helper()
	c.Session = uuid.MustParse(session)
	ticket, err := f.key.MintTicket(c, now, now)
	if err != nil {
		t.Fatalf("MintTicket: %v", err)
	}
	return ticket.Reveal()
}

// startFor starts a session with the keeper for the user the product names, as
// its server does with its key, and returns the reply with the caller its
// ticket names: the session's user, workspace and the session.
func (f fixture) startFor(t *testing.T, reference string) (startReply, access.Caller) {
	t.Helper()
	rec := f.send("POST", "/v1/sessions", f.productKey(t), startFrom(reference, keeperID, uuid.NewV7()))
	if rec.Code != nethttp.StatusCreated {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	reply := decode[startReply](t, rec)
	c, err := f.key.Verify(reply.Ticket, now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	return reply, c
}

func (f fixture) do(method, path, bearer string) *httptest.ResponseRecorder {
	return f.send(method, path, bearer, "")
}

func (f fixture) send(method, path, bearer, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

type agentList struct {
	Agents []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"agents"`
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(rec.Body).Decode(&v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func assertProblem(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d", rec.Code, status)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	p := decode[problem](t, rec)
	if p.Status != status || p.Title != nethttp.StatusText(status) || p.Type != "about:blank" {
		t.Errorf("problem = %+v, want status %d with its title", p, status)
	}
}

// assertProblemType checks a problem of a type of its own, which a client
// tells apart from others with the same status by the type alone.
func assertProblemType(t *testing.T, rec *httptest.ResponseRecorder, status int, typ string) problem {
	t.Helper()
	if rec.Code != status || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Errorf("status %d, %s; want a %d problem", rec.Code, rec.Header().Get("Content-Type"), status)
	}
	p := decode[problem](t, rec)
	if p.Type != typ || p.Status != status || p.Title == "" {
		t.Errorf("problem = %+v, want type %s with status %d and a title", p, typ, status)
	}
	return p
}

func TestListAgentsReturnsTheCallersAgentsWithoutTheirPersonas(t *testing.T) {
	f := newFixture(t)
	rec := f.do("GET", "/v1/agents", f.productKey(t))

	if rec.Code != nethttp.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "secret persona") {
		t.Error("the list exposes an agent's persona")
	}
	got := decode[agentList](t, rec)
	if len(got.Agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(got.Agents))
	}
	k := got.Agents[1]
	if k.ID != keeperID.String() || k.Name != "Lighthouse keeper" || k.Description != "Talks ships through the fog." {
		t.Errorf("lighthouse keeper = %+v", k)
	}
}

func TestEveryConversationRouteNeedsACredential(t *testing.T) {
	f := newFixture(t)
	for _, route := range []string{"GET /v1/agents", "POST /v1/sessions", "POST /v1/sessions/" + uuid.NewV7().String() + "/turns", "POST /v1/sessions/" + uuid.NewV7().String() + "/end"} {
		t.Run(route, func(t *testing.T) {
			method, path, _ := strings.Cut(route, " ")
			assertProblem(t, f.do(method, path, ""), nethttp.StatusUnauthorized)
		})
	}
}

// The session's end is the one route in a session that takes the product's
// key, so its server can end a session whose ticket was stolen.
func TestEveryRouteInASessionButItsEndRefusesAProductKey(t *testing.T) {
	f := newFixture(t)
	key := f.productKey(t)
	session := decode[sessionReply](t, f.send("POST", "/v1/sessions", key, `{"agent_id":"`+keeperID.String()+`","request_id":"`+uuid.NewV7().String()+`","user":"aiko-42"}`))
	for _, route := range []string{"POST /v1/sessions/" + session.ID + "/turns"} {
		t.Run(route, func(t *testing.T) {
			method, path, _ := strings.Cut(route, " ")
			assertProblem(t, f.send(method, path, key, `{"request_id":"`+uuid.NewV7().String()+`","words":"Hello."}`), nethttp.StatusUnauthorized)
		})
	}
}

// A key made only to publish agents runs no session, so one leaked from where
// agents are published can't start sessions on the product's bill.
func TestEveryProductRouteNeedsAKeyThatMayRunSessions(t *testing.T) {
	f := newFixture(t)
	publisher, _, err := f.keys.Create(context.Background(), workspace, access.PublishAgents)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	session := uuid.NewV7().String()
	for _, route := range []string{"GET /v1/agents", "GET /v1/voices", "POST /v1/sessions", "GET /v1/users/aiko-42/sessions", "GET /v1/sessions/" + session, "POST /v1/sessions/" + session + "/end"} {
		t.Run(route, func(t *testing.T) {
			method, path, _ := strings.Cut(route, " ")
			rec := f.do(method, path, publisher.Reveal())
			if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer error="insufficient_scope", scope="run_sessions"` {
				t.Errorf("WWW-Authenticate = %q, want insufficient_scope naming run_sessions", got)
			}
			assertProblem(t, rec, nethttp.StatusForbidden)
		})
	}
}

func TestAnotherWorkspacesKeyListsNoAgents(t *testing.T) {
	f := newFixture(t)
	other := uuid.MustParse("01a1de8a-0000-7000-8000-000000000002")
	rec := f.do("GET", "/v1/agents", f.productKeyFor(t, other))

	if got := decode[agentList](t, rec); len(got.Agents) != 0 {
		t.Errorf("agents = %+v, want none", got.Agents)
	}
}

// brokenStore is a store of agent versions whose every read fails.
type brokenStore struct{ agents.Versions }

func (brokenStore) Newest(context.Context, uuid.UUID) ([]agents.Version, error) {
	return nil, errors.New("connection refused by 10.0.0.7:5432")
}

func TestAFailedReadAnswers500WithoutTheReason(t *testing.T) {
	f := fixtureOver(t, brokenStore{})
	rec := f.do("GET", "/v1/agents", f.productKey(t))

	if rec.Code != nethttp.StatusInternalServerError || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Errorf("status %d, %s; want a 500 problem", rec.Code, rec.Header().Get("Content-Type"))
	}
	if strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("body %s gives away the reason", rec.Body.String())
	}
}

type sessionReply struct {
	ID           string     `json:"id"`
	AgentID      string     `json:"agent_id"`
	AgentVersion int        `json:"agent_version"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at"`
}

// startFrom is the body a product's server sends to start a session for the
// user it names.
func startFrom(reference string, agent, request uuid.UUID) string {
	return `{"agent_id":"` + agent.String() + `","request_id":"` + request.String() + `","user":"` + reference + `"}`
}

func startBody(agent, request uuid.UUID) string {
	return startFrom("aiko-42", agent, request)
}

func TestStartingASessionAnswers201ThenTheSameSessionWith200(t *testing.T) {
	f := newFixture(t)
	bearer := f.productKey(t)
	body := startBody(keeperID, uuid.NewV7())

	first := f.send("POST", "/v1/sessions", bearer, body)
	if first.Code != nethttp.StatusCreated {
		t.Fatalf("first start: status = %d, want 201: %s", first.Code, first.Body)
	}
	created := decode[sessionReply](t, first)
	id, err := uuid.Parse(created.ID)
	if err != nil || id[6]>>4 != 7 || created.AgentID != keeperID.String() || created.AgentVersion != 1 || created.StartedAt.IsZero() {
		t.Errorf("session = %+v, want a UUIDv7 ID on the keeper's version 1 with a start time", created)
	}

	again := f.send("POST", "/v1/sessions", bearer, body)
	if again.Code != nethttp.StatusOK {
		t.Fatalf("repeat: status = %d, want 200: %s", again.Code, again.Body)
	}
	if repeat := decode[sessionReply](t, again); repeat != created {
		t.Errorf("repeat = %+v, want the first session %+v", repeat, created)
	}
}

func TestAProductStartsASessionForTheUserItNamesWithItsKey(t *testing.T) {
	f := newFixture(t)
	key := f.productKey(t)
	if rec := f.do("GET", "/v1/agents", key); rec.Code != nethttp.StatusOK || len(decode[agentList](t, rec).Agents) != 2 {
		t.Fatalf("list agents with the key: %d %s; want the workspace's two agents", rec.Code, rec.Body)
	}
	request := uuid.NewV7().String()
	body := `{"agent_id":"` + keeperID.String() + `","request_id":"` + request + `","user":"aiko-42"}`
	first := f.send("POST", "/v1/sessions", key, body)
	again := f.send("POST", "/v1/sessions", key, body)
	if first.Code != nethttp.StatusCreated || again.Code != nethttp.StatusOK || decode[sessionReply](t, again) != decode[sessionReply](t, first) {
		t.Errorf("start, then again: %d, %d; want 201, then 200 with the same session", first.Code, again.Code)
	}
}

func TestAStartWithAKeyMustNameTheUser(t *testing.T) {
	f := newFixture(t)
	key := f.productKey(t)
	for name, user := range map[string]string{"missing": "", "two lines": `aiko\n42`} {
		body := `{"agent_id":"` + keeperID.String() + `","request_id":"` + uuid.NewV7().String() + `","user":"` + user + `"}`
		if rec := f.send("POST", "/v1/sessions", key, body); rec.Code != nethttp.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", name, rec.Code, rec.Body)
		}
	}
}

func TestAgentsOutOfReachAnswer404Alike(t *testing.T) {
	f := newFixture(t)
	other := uuid.MustParse("01a1de8a-0000-7000-8000-000000000002")
	tests := map[string]struct {
		workspace uuid.UUID
		agent     uuid.UUID
	}{
		"unknown agent":       {workspace, uuid.NewV7()},
		"another workspace's": {other, keeperID},
	}
	var bodies []string
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := f.send("POST", "/v1/sessions", f.productKeyFor(t, tt.workspace), startBody(tt.agent, uuid.NewV7()))
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

func TestAMalformedStartIsA400(t *testing.T) {
	f := newFixture(t)
	bearer := f.productKey(t)
	request := uuid.NewV7().String()
	for name, body := range map[string]string{
		"not JSON":         "agent please",
		"empty":            "",
		"no request ID":    `{"agent_id":"` + keeperID.String() + `","user":"aiko-42"}`,
		"no agent":         `{"request_id":"` + request + `","user":"aiko-42"}`,
		"agent not a UUID": `{"agent_id":"keeper","request_id":"` + request + `","user":"aiko-42"}`,
		"nil request ID":   startBody(keeperID, uuid.Nil()),
	} {
		t.Run(name, func(t *testing.T) {
			assertProblem(t, f.send("POST", "/v1/sessions", bearer, body), nethttp.StatusBadRequest)
		})
	}
}

func TestAStartBodyOverTheLimitIsA413(t *testing.T) {
	f := newFixture(t)
	// Past the body limit, which fits the default session context with room to spare.
	body := `{"agent_id":"` + keeperID.String() + `","request_id":"` + uuid.NewV7().String() + `","user":"aiko-42","x":"` + strings.Repeat("a", 300<<10) + `"}`
	assertProblem(t, f.send("POST", "/v1/sessions", f.productKey(t), body), nethttp.StatusRequestEntityTooLarge)
}

// keeperExpectingDocuments is a fixture whose keeper expects a document named resume from the
// product, and may be sent one named ship_log that only its summary reads.
func keeperExpectingDocuments(t *testing.T, sessionContextChars int) fixture {
	t.Helper()
	store := fake.New()
	if _, err := agents.Publish(context.Background(), store, workspace, []agents.Agent{
		{ID: keeperID, Name: "Lighthouse keeper", SessionContext: []string{"resume"}, Persona: "secret persona", Summary: "Note the ships.", SummaryDocuments: []string{"ship_log"}},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	key, err := ticket.NewKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	svc := conversation.NewService(agents.NewCatalog(store), sessionsfake.New(), nil, nil, usercontext.NewService(itemsfake.New()),
		conversation.Settings{UserContextChars: 1000, SessionContextChars: sessionContextChars})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	keys := productkey.NewKeys(keysfake.New())
	return fixture{handler: handlerFor(svc, key, keys, log), key: key, keys: keys}
}

func startWithSessionContext(blocks string) string {
	return `{"agent_id":"` + keeperID.String() + `","request_id":"` + uuid.NewV7().String() + `","user":"aiko-42","session_context":` + blocks + `}`
}

func TestStartingWithTheExpectedSessionContextAnswers201(t *testing.T) {
	f := keeperExpectingDocuments(t, 1000)
	rec := f.send("POST", "/v1/sessions", f.productKey(t),
		startWithSessionContext(`[{"name":"resume","content":"Deckhand, two seasons."}]`))
	if rec.Code != nethttp.StatusCreated {
		t.Errorf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
}

func TestSessionContextThatDoesntMatchTheAgentIsA422NamingTheProblem(t *testing.T) {
	f := keeperExpectingDocuments(t, 1000)
	rec := f.send("POST", "/v1/sessions", f.productKey(t),
		startWithSessionContext(`[{"name":"page","content":"Pricing"}]`))
	p := assertProblemType(t, rec, nethttp.StatusUnprocessableEntity, "/v1/problems/session-context-mismatch")
	if !strings.Contains(p.Detail, `"resume"`) || !strings.Contains(p.Detail, `"page"`) {
		t.Errorf("detail = %q, want the missing resume and the unexpected page named", p.Detail)
	}
}

func TestTheBodyLimitAlwaysFitsTheSessionContextLimit(t *testing.T) {
	// The worst case: characters outside the basic plane, each escaped as 12 bytes of JSON.
	const chars = 30_000
	f := keeperExpectingDocuments(t, chars)
	content := strings.Repeat(`\ud83d\ude00`, chars)
	rec := f.send("POST", "/v1/sessions", f.productKey(t),
		startWithSessionContext(`[{"name":"resume","content":"`+content+`"}]`))
	if rec.Code != nethttp.StatusCreated {
		t.Errorf("status = %d for %d escaped characters within a limit of %d, want 201: %.200s", rec.Code, chars, chars, rec.Body)
	}
}

func TestSessionContextAtTheLimitStartsAndOneCharacterMoreIsA413(t *testing.T) {
	// The service's default, 280,000 characters, split between a document the conversation reads
	// and one only the summary reads, since the limit counts both.
	const limit = 280_000
	f := keeperExpectingDocuments(t, limit)
	for extra, want := range map[int]int{0: nethttp.StatusCreated, 1: nethttp.StatusRequestEntityTooLarge} {
		blocks := `[{"name":"resume","content":"` + strings.Repeat("a", limit/2) + `"},{"name":"ship_log","content":"` + strings.Repeat("b", limit/2+extra) + `"}]`
		if rec := f.send("POST", "/v1/sessions", f.productKey(t), startWithSessionContext(blocks)); rec.Code != want {
			t.Errorf("%d characters: status = %d, want %d: %.200s", limit+extra, rec.Code, want, rec.Body)
		}
	}
}

func TestSessionContextOverTheCharacterLimitIsA413(t *testing.T) {
	f := keeperExpectingDocuments(t, 10)
	assertProblem(t, f.send("POST", "/v1/sessions", f.productKey(t),
		startWithSessionContext(`[{"name":"resume","content":"Deckhand, two seasons."}]`)), nethttp.StatusRequestEntityTooLarge)
}

func TestARequestIDReusedForAnotherAgentIsA422(t *testing.T) {
	f := newFixture(t)
	bearer := f.productKey(t)
	request := uuid.NewV7()
	if rec := f.send("POST", "/v1/sessions", bearer, startBody(keeperID, request)); rec.Code != nethttp.StatusCreated {
		t.Fatalf("first start: status = %d, want 201", rec.Code)
	}

	assertProblemType(t, f.send("POST", "/v1/sessions", bearer, startBody(captainID, request)), nethttp.StatusUnprocessableEntity, "/v1/problems/request-reused")
}

func TestARepeatWithDifferentSessionContextIsA422(t *testing.T) {
	f := keeperExpectingDocuments(t, 1000)
	bearer := f.productKey(t)
	request := uuid.NewV7()
	start := func(resume string) *httptest.ResponseRecorder {
		return f.send("POST", "/v1/sessions", bearer, `{"agent_id":"`+keeperID.String()+`","request_id":"`+request.String()+
			`","user":"aiko-42","session_context":[{"name":"resume","content":"`+resume+`"}]}`)
	}
	if rec := start("Deckhand, two seasons."); rec.Code != nethttp.StatusCreated {
		t.Fatalf("first start: status = %d, want 201", rec.Code)
	}

	p := assertProblemType(t, start("Bosun."), nethttp.StatusUnprocessableEntity, "/v1/problems/request-reused")
	if !strings.Contains(p.Detail, "different session context") {
		t.Errorf("detail = %q, want it to say the session context differs", p.Detail)
	}
	if rec := start("Deckhand, two seasons."); rec.Code != nethttp.StatusOK {
		t.Errorf("the first request again: status = %d, want 200", rec.Code)
	}
}

// brokenItems is a store of context items whose every read fails.
type brokenItems struct{}

func (brokenItems) Current(context.Context, uuid.UUID, uuid.UUID, []string, uuid.UUID, int) ([]usercontext.Item, error) {
	return nil, errors.New("connection refused by 10.0.0.7:5432")
}

func (brokenItems) ByID(context.Context, uuid.UUID, uuid.UUID, []uuid.UUID) ([]usercontext.Item, error) {
	return nil, errors.New("connection refused by 10.0.0.7:5432")
}

func (brokenItems) AddSummary(context.Context, usercontext.Item) (usercontext.Item, bool, error) {
	return usercontext.Item{}, false, errors.New("connection refused by 10.0.0.7:5432")
}

func (brokenItems) SessionSummary(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (usercontext.Item, bool, error) {
	return usercontext.Item{}, false, errors.New("connection refused by 10.0.0.7:5432")
}

// brokenSessions is a store of sessions whose every start fails.
type brokenSessions struct{}

func (brokenSessions) ForRequest(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (conversation.Session, bool, error) {
	return conversation.Session{}, false, nil
}

func (brokenSessions) Start(context.Context, conversation.Session, uuid.UUID, string) (conversation.Session, bool, error) {
	return conversation.Session{}, false, errors.New("connection refused by 10.0.0.7:5432")
}

func (brokenSessions) ByID(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (conversation.Session, bool, error) {
	return conversation.Session{}, false, errors.New("connection refused by 10.0.0.7:5432")
}

func (brokenSessions) InWorkspace(context.Context, uuid.UUID, uuid.UUID) (conversation.Session, bool, error) {
	return conversation.Session{}, false, errors.New("connection refused by 10.0.0.7:5432")
}

func (brokenSessions) End(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (conversation.Session, bool, error) {
	return conversation.Session{}, false, errors.New("connection refused by 10.0.0.7:5432")
}

func (brokenSessions) SaveSummaryData(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, json.RawMessage) error {
	return errors.New("connection refused by 10.0.0.7:5432")
}

func (brokenSessions) SettleSummary(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, conversation.SummaryStatus) error {
	return errors.New("connection refused by 10.0.0.7:5432")
}

func (brokenSessions) Touch(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (bool, error) {
	return false, errors.New("connection refused by 10.0.0.7:5432")
}

// The user is new to the product's workspace: nothing is known of them yet.
func (brokenSessions) UserByReference(context.Context, uuid.UUID, string) (uuid.UUID, bool, error) {
	return uuid.Nil(), false, nil
}

// brokenLookup is a store of sessions whose every user lookup fails.
type brokenLookup struct{ brokenSessions }

func (brokenLookup) UserByReference(context.Context, uuid.UUID, string) (uuid.UUID, bool, error) {
	return uuid.Nil(), false, errors.New("connection refused by 10.0.0.7:5432")
}

func (brokenSessions) Reference(context.Context, uuid.UUID, uuid.UUID) (string, bool, error) {
	return "", false, errors.New("connection refused by 10.0.0.7:5432")
}

func (brokenSessions) ListForUser(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int) ([]conversation.Session, error) {
	return nil, errors.New("connection refused by 10.0.0.7:5432")
}

func (brokenSessions) EndIdle(context.Context, time.Duration) ([]uuid.UUID, error) {
	return nil, errors.New("connection refused by 10.0.0.7:5432")
}

func TestAFailedStartAnswersWithoutTheReason(t *testing.T) {
	tests := map[string]struct {
		f      fixture
		status int
	}{
		"agents unreadable":       {fixtureOver(t, brokenStore{}), nethttp.StatusInternalServerError},
		"session not saved":       {fixtureOverStores(t, newFixtureStore(t), brokenSessions{}, itemsfake.New()), nethttp.StatusInternalServerError},
		"user context not loaded": {fixtureOverStores(t, newFixtureStore(t), sessionsfake.New(), brokenItems{}), nethttp.StatusServiceUnavailable},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			f := tt.f
			rec := f.send("POST", "/v1/sessions", f.productKey(t), startBody(keeperID, uuid.NewV7()))
			assertProblem(t, rec, tt.status)
			if strings.Contains(rec.Body.String(), "10.0.0.7") {
				t.Errorf("body %s gives away the reason", rec.Body.String())
			}
		})
	}
}

func TestAStartWhoseUserCantBeLookedUpIsA503WithoutTheReason(t *testing.T) {
	f := fixtureOverStores(t, newFixtureStore(t), brokenLookup{}, itemsfake.New())
	body := `{"agent_id":"` + keeperID.String() + `","request_id":"` + uuid.NewV7().String() + `","user":"aiko-42"}`
	rec := f.send("POST", "/v1/sessions", f.productKey(t), body)
	assertProblem(t, rec, nethttp.StatusServiceUnavailable)
	if strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("body %s gives away the reason", rec.Body.String())
	}
}

func TestAProductSeesAndStartsOnlyItsOwnWorkspacesAgents(t *testing.T) {
	store := newFixtureStore(t)
	other, theirs := uuid.MustParse("01a1de8a-0000-7000-8000-000000000002"), uuid.NewV7()
	if _, err := agents.Publish(context.Background(), store, other, []agents.Agent{{ID: theirs, Name: "Harbour pilot", Persona: "secret persona"}}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	f := fixtureOver(t, store)
	key, _, err := f.keys.Create(context.Background(), other, access.RunSessions)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if list := decode[agentList](t, f.do("GET", "/v1/agents", key.Reveal())); len(list.Agents) != 1 || list.Agents[0].ID != theirs.String() {
		t.Errorf("agents = %+v; want only the other workspace's own", list.Agents)
	}
	body := `{"agent_id":"` + keeperID.String() + `","request_id":"` + uuid.NewV7().String() + `","user":"aiko-42"}`
	assertProblem(t, f.send("POST", "/v1/sessions", key.Reveal(), body), nethttp.StatusNotFound)
}

func TestTheAgentListNamesTheDocumentsOnlyTheSummaryReads(t *testing.T) {
	f := keeperExpectingDocuments(t, 1000)
	rec := f.do("GET", "/v1/agents", f.productKey(t))
	var got struct {
		Agents []struct {
			SummaryDocuments []string `json:"summary_documents"`
		} `json:"agents"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil || len(got.Agents) != 1 || !slices.Equal(got.Agents[0].SummaryDocuments, []string{"ship_log"}) {
		t.Errorf("listed %+v, %v; want the keeper's ship_log", got, err)
	}
}

func TestTheAgentListSaysWhatAProductMustSendAtStart(t *testing.T) {
	store := fake.New()
	if _, err := agents.Publish(context.Background(), store, workspace, []agents.Agent{
		{ID: keeperID, Name: "Lighthouse keeper", SessionContext: []string{"resume"}, Persona: "secret persona"},
		{ID: captainID, Name: "Ferry captain", Persona: "secret persona"},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	f := fixtureOver(t, store)
	rec := f.do("GET", "/v1/agents", f.productKey(t))

	var got struct {
		Agents []struct {
			ID             string    `json:"id"`
			SessionContext *[]string `json:"session_context"`
		} `json:"agents"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil || len(got.Agents) != 2 {
		t.Fatalf("decode: %v, %+v", err, got)
	}
	captain, keeper := got.Agents[0], got.Agents[1]
	if keeper.SessionContext == nil || !slices.Equal(*keeper.SessionContext, []string{"resume"}) {
		t.Errorf("keeper's session context = %v, want [resume]", keeper.SessionContext)
	}
	if captain.SessionContext == nil || len(*captain.SessionContext) != 0 {
		t.Errorf("captain's session context = %v, want an empty list, never missing", captain.SessionContext)
	}
}
