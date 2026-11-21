package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	nethttp "net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	agentshttp "github.com/mikaclements/voice-guide/internal/agents/http"
	"github.com/mikaclements/voice-guide/internal/agents/store/fake"
	"github.com/mikaclements/voice-guide/internal/api"
	"github.com/mikaclements/voice-guide/internal/productkey"
	keysfake "github.com/mikaclements/voice-guide/internal/productkey/store/fake"
	"github.com/mikaclements/voice-guide/internal/ticket"
)

var (
	workspace = uuid.MustParse("01a1de8a-0000-7000-8000-000000000001")
	elsewhere = uuid.MustParse("01a1de8a-0000-7000-8000-000000000002")
	keeperID  = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a1")
	pilotID   = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a2")
)

type fixture struct {
	rt    *api.Router
	key   ticket.Key
	keys  *productkey.Keys
	store agents.Versions
}

// newFixture serves the agents module's routes over the store of versions,
// taking product keys kept in a fake store.
func newFixture(t *testing.T, store agents.Versions) fixture {
	t.Helper()
	return newFixtureWithVoices(t, store, &voiceList{names: []string{"af_heart", "bf_emma"}})
}

// newFixtureWithVoices is newFixture with the text-to-speech service's voices, as the publish route
// checks them.
func newFixtureWithVoices(t *testing.T, store agents.Versions, voices agents.VoiceList) fixture {
	t.Helper()
	key, err := ticket.NewKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	f := fixture{key: key, keys: productkey.NewKeys(keysfake.New()), store: store}
	f.rt = api.NewRouter(key, f.keys, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	agentshttp.Register(f.rt, agents.NewCatalog(store), agents.NewPublisher(store, []string{"profile"}, voices))
	return f
}

// get sends a GET for path with bearer as its credential.
func (f fixture) get(path, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	f.rt.ServeHTTP(rec, req)
	return rec
}

// keyWith returns a working key for the workspace holding the permissions.
func (f fixture) keyWith(t *testing.T, permissions access.Permissions) string {
	t.Helper()
	s, _, err := f.keys.Create(context.Background(), workspace, permissions)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return s.Reveal()
}

func publish(t *testing.T, store agents.Versions, ws uuid.UUID, a agents.Agent) {
	t.Helper()
	if _, err := agents.Publish(context.Background(), store, ws, []agents.Agent{a}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

// publishedTwice returns a store holding two versions of an agent in the
// workspace, every optional setting set, and an agent of another workspace.
func publishedTwice(t *testing.T) agents.Versions {
	t.Helper()
	store := fake.New()
	keeper := agents.Agent{
		ID: keeperID, Name: "Lighthouse keeper", Description: "Talks ships through the fog.", Reads: []string{"profile"}, SessionContext: []string{"logbook"},
		Persona: "You keep the light.", Summary: "Note the ships that passed.", SummaryModel: "claude-test", SummarySchema: json.RawMessage(`{"type":"object"}`),
		SummaryDocuments: []string{"chart"}, Opens: true, Model: "claude-test", Voice: "bf_emma", Speed: 1.1,
	}
	publish(t, store, workspace, keeper)
	keeper.Persona = "You keep the light burning through the storm."
	publish(t, store, workspace, keeper)
	publish(t, store, elsewhere, agents.Agent{ID: pilotID, Name: "Harbour pilot", Persona: "You bring ships in."})
	return store
}

type problem struct {
	Type   string `json:"type"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func TestAnAgentIsReadAtItsNewestVersionInItsFilesShapes(t *testing.T) {
	f := newFixture(t, publishedTwice(t))
	rec := f.get("/v1/agents/"+keeperID.String(), f.keyWith(t, access.PublishAgents))
	var got struct {
		Version        int
		PublishedAt    string `json:"published_at"`
		Persona, Opens string
		SessionContext []string `json:"session_context"`
		Model, Voice   *string
		Speed          *float64
		Summary        *struct {
			Instruction string
			Model       *string
			Schema      json.RawMessage
			Documents   []string
		}
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil || rec.Code != nethttp.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %s (%v); want 200 with JSON", rec.Code, rec.Header().Get("Content-Type"), err)
	}
	if got.Version != 2 || got.Persona != "You keep the light burning through the storm." || got.Opens != "agent" || !slices.Equal(got.SessionContext, []string{"logbook"}) ||
		got.Model == nil || *got.Model != "claude-test" || got.Voice == nil || *got.Voice != "bf_emma" || got.Speed == nil || *got.Speed != 1.1 {
		t.Errorf("read %+v; want the keeper's second version with its settings as its file writes them", got)
	}
	if s := got.Summary; s == nil || s.Instruction != "Note the ships that passed." || s.Model == nil || *s.Model != "claude-test" ||
		string(s.Schema) != `{"type":"object"}` || !slices.Equal(s.Documents, []string{"chart"}) {
		t.Errorf("summary %+v; want the file's summary mapping", got.Summary)
	}
	if at, err := time.Parse(time.RFC3339Nano, got.PublishedAt); err != nil || at.IsZero() || !strings.HasSuffix(got.PublishedAt, "Z") {
		t.Errorf("published_at %q (%v); want when it was published, in UTC", got.PublishedAt, err)
	}
}

// What the README promises of an agent whose file sets nothing optional: every
// field there, a setting left out null and a list left out empty.
func TestAnAgentWithNothingOptionalReadsBackNullsAndEmptyLists(t *testing.T) {
	store := fake.New()
	publish(t, store, workspace, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You keep the light."})
	f := newFixture(t, store)
	var got map[string]any
	if err := json.NewDecoder(f.get("/v1/agents/"+keeperID.String(), f.keyWith(t, access.PublishAgents)).Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	keys := slices.Sorted(maps.Keys(got))
	want := []string{"description", "id", "model", "name", "opens", "persona", "published_at", "reads", "session_context", "speed", "summary", "version", "voice"}
	if !slices.Equal(keys, want) {
		t.Errorf("fields %v; want %v", keys, want)
	}
	for _, list := range []string{"reads", "session_context"} {
		if l, ok := got[list].([]any); !ok || len(l) != 0 {
			t.Errorf("%s = %#v; want []", list, got[list])
		}
	}
	for _, unset := range []string{"summary", "model", "voice", "speed"} {
		if got[unset] != nil {
			t.Errorf("%s = %#v; want null", unset, got[unset])
		}
	}
	if got["opens"] != "user" || got["description"] != "" {
		t.Errorf("opens %#v, description %#v; want user and empty", got["opens"], got["description"])
	}
}

// Another workspace's agent is answered as one never published, so a key
// learns nothing of other workspaces' agents.
func TestAnAgentOutOfReachIsNotFoundAlike(t *testing.T) {
	f := newFixture(t, publishedTwice(t))
	key := f.keyWith(t, access.PublishAgents)
	for name, id := range map[string]string{"another workspace's": pilotID.String(), "never published": uuid.NewV7().String(), "not a UUID": "keeper"} {
		rec := f.get("/v1/agents/"+id, key)
		var p problem
		if err := json.NewDecoder(rec.Body).Decode(&p); err != nil || rec.Code != nethttp.StatusNotFound || p.Type != "about:blank" || p.Detail != "no such agent" {
			t.Errorf("%s: %d %+v (%v); want 404, no such agent", name, rec.Code, p, err)
		}
	}
}

func TestReadingAnAgentNeedsAKeyThatMayPublish(t *testing.T) {
	f := newFixture(t, publishedTwice(t))
	path := "/v1/agents/" + keeperID.String()
	rec := f.get(path, f.keyWith(t, access.RunSessions))
	if got := rec.Header().Get("WWW-Authenticate"); rec.Code != nethttp.StatusForbidden || got != `Bearer error="insufficient_scope", scope="publish_agents"` {
		t.Errorf("a key that only runs sessions: %d, WWW-Authenticate %q; want 403 naming publish_agents", rec.Code, got)
	}
	ticket, err := f.key.MintTicket(access.Caller{Workspace: workspace, User: uuid.NewV7(), Session: uuid.NewV7()}, time.Now(), time.Now())
	if err != nil {
		t.Fatalf("MintTicket: %v", err)
	}
	if rec := f.get(path, ticket.Reveal()); rec.Code != nethttp.StatusUnauthorized {
		t.Errorf("a ticket: %d; want 401", rec.Code)
	}
	req := httptest.NewRequest("GET", path, nil)
	rec = httptest.NewRecorder()
	f.rt.ServeHTTP(rec, req)
	if rec.Code != nethttp.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("no credential: %d, WWW-Authenticate %q; want 401 asking for a bearer credential", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}

// brokenStore is a store of agent versions whose every read fails.
type brokenStore struct{ agents.Versions }

func (brokenStore) Newest(context.Context, uuid.UUID) ([]agents.Version, error) {
	return nil, errors.New("connection refused by 10.0.0.7:5432")
}

func TestAnAgentThatCantBeReadIsUnavailableWithoutTheReason(t *testing.T) {
	f := newFixture(t, brokenStore{})
	rec := f.get("/v1/agents/"+keeperID.String(), f.keyWith(t, access.PublishAgents))
	if rec.Code != nethttp.StatusServiceUnavailable || rec.Header().Get("Content-Type") != "application/problem+json" || strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("%d %s %s; want a 503 problem without the reason", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
}
