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
	rt   *api.Router
	key  ticket.Key
	keys *productkey.Keys
}

// newFixture serves the agents module's routes over the store of versions,
// taking product keys kept in a fake store.
func newFixture(t *testing.T, store agents.Versions) fixture {
	t.Helper()
	key, err := ticket.NewKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	f := fixture{key: key, keys: productkey.NewKeys(keysfake.New())}
	f.rt = api.NewRouter(key, f.keys, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	agentshttp.Register(f.rt, agents.NewCatalog(store))
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
// workspace, and an agent of another workspace.
func publishedTwice(t *testing.T) agents.Versions {
	t.Helper()
	store := fake.New()
	keeper := agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Reads: []string{"profile"}, Persona: "You keep the light.", Voice: "bf_emma", Speed: 1.1}
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

func TestAnAgentIsReadAtItsNewestVersionWithItsNumber(t *testing.T) {
	f := newFixture(t, publishedTwice(t))
	rec := f.get("/v1/agents/"+keeperID.String(), f.keyWith(t, access.PublishAgents))
	var got struct {
		ID            string
		Version       int
		PublishedAt   time.Time `json:"published_at"`
		Name, Persona string
		Reads         []string
		Voice         string
		Speed         float64
		SummarySchema json.RawMessage `json:"summary_schema"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil || rec.Code != nethttp.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %s (%v); want 200 with JSON", rec.Code, rec.Header().Get("Content-Type"), err)
	}
	if got.ID != keeperID.String() || got.Version != 2 || got.Persona != "You keep the light burning through the storm." || got.Name != "Lighthouse keeper" ||
		!slices.Equal(got.Reads, []string{"profile"}) || got.Voice != "bf_emma" || got.Speed != 1.1 || got.PublishedAt.IsZero() || string(got.SummarySchema) != "null" {
		t.Errorf("read %+v; want the keeper's second version as published, with when", got)
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
}

// brokenStore is a store of agent versions whose every read fails.
type brokenStore struct{ agents.Versions }

func (brokenStore) Newest(context.Context, uuid.UUID) ([]agents.Version, error) {
	return nil, errors.New("connection refused by 10.0.0.7:5432")
}

func TestAnAgentThatCantBeReadIsUnavailableWithoutTheReason(t *testing.T) {
	f := newFixture(t, brokenStore{})
	rec := f.get("/v1/agents/"+keeperID.String(), f.keyWith(t, access.PublishAgents))
	if rec.Code != nethttp.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("%d %s; want 503 without the reason", rec.Code, rec.Body)
	}
}
