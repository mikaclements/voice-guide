package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/api"
	"github.com/mikaclements/voice-guide/internal/productkey"
	keysfake "github.com/mikaclements/voice-guide/internal/productkey/store/fake"
	"github.com/mikaclements/voice-guide/internal/ticket"
)

func TestEveryRouteGivesItsHandlerADeadline(t *testing.T) {
	key, err := ticket.NewKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	rt := api.NewRouter(key, nil, func() time.Time { return now }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var hasDeadline bool
	rt.HandleTicket("GET /v1/sessions/{id}/probe", api.RequestTimeout, func(_ http.ResponseWriter, r *http.Request, _ access.Caller) {
		_, hasDeadline = r.Context().Deadline()
	})
	sid := uuid.NewV7()
	raw, ok, err := rt.Ticket(access.Caller{Workspace: uuid.NewV7(), User: uuid.NewV7(), Session: sid}, now)
	if err != nil || !ok {
		t.Fatalf("Ticket: %v, minted %v", err, ok)
	}
	req := httptest.NewRequest("GET", "/v1/sessions/"+sid.String()+"/probe", nil)
	req.Header.Set("Authorization", "Bearer "+raw.Reveal())
	rt.ServeHTTP(httptest.NewRecorder(), req)
	if !hasDeadline {
		t.Error("the handler's context has no deadline")
	}
}

var (
	now    = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	caller = access.Caller{
		Workspace: uuid.MustParse("01a1de8a-0000-7000-8000-000000000001"),
		User:      uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a1"),
	}
	session = uuid.NewV7().String()
)

// routes are the method and path shapes of the API's routes, each with a path
// it serves, registered here on stub handlers as the adapters register them:
// the Handle methods add the credential check whatever the route, and these
// show it holds for every shape the API has. The routes a product's server
// calls take its key, those in a session the session's ticket, and the
// session's end both, and the ticket shortly past its lifetime too. Each adapter's tests
// check that the routes it really registers need a ticket or a key.
var routes = []struct {
	pattern, path         string
	keys, tickets, ending bool
}{
	{"GET /v1/agents", "/v1/agents", true, false, false},
	{"POST /v1/sessions", "/v1/sessions", true, false, false},
	{"POST /v1/sessions/{id}/turns", "/v1/sessions/" + session + "/turns", false, true, false},
	{"POST /v1/sessions/{id}/end", "/v1/sessions/" + session + "/end", true, true, true},
	{"POST /v1/sessions/{id}/audio", "/v1/sessions/" + session + "/audio", false, true, false},
	{"DELETE /v1/sessions/{id}/audio/{conn}", "/v1/sessions/" + session + "/audio/" + uuid.NewV7().String(), false, true, false},
}

type fixture struct {
	rt      *api.Router
	key     ticket.Key
	keys    *productkey.Keys
	store   *keysfake.Keys
	logs    *strings.Builder
	reached map[string]access.Caller // by pattern, the caller each handler was given
	at      time.Time                // the router's clock, now unless a test moves it
}

// newFixture returns the API's router with every route in routes registered,
// each recording the caller it reaches.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWithKey(t, "0123456789abcdef0123456789abcdef")
}

// newFixtureWithKey is newFixture signing with secret.
func newFixtureWithKey(t *testing.T, secret string) *fixture {
	t.Helper()
	f := &fixture{key: newKey(t, secret), store: keysfake.New(), logs: &strings.Builder{}, reached: map[string]access.Caller{}, at: now}
	f.keys = productkey.NewKeys(f.store)
	f.rt = api.NewRouter(f.key, f.keys, func() time.Time { return f.at }, slog.New(slog.NewTextHandler(f.logs, nil)))
	for _, r := range routes {
		record := func(_ http.ResponseWriter, _ *http.Request, c access.Caller) { f.reached[r.pattern] = c }
		switch {
		case r.keys && r.tickets:
			f.rt.HandleSessionEnd(r.pattern, api.RequestTimeout, record)
		case r.keys:
			f.rt.HandleProduct(r.pattern, api.RequestTimeout, record)
		default:
			f.rt.HandleTicket(r.pattern, api.RequestTimeout, record)
		}
	}
	return f
}

// productKey returns a working key for the caller's workspace.
func (f *fixture) productKey(t *testing.T) string {
	t.Helper()
	s, _, err := f.keys.Create(context.Background(), caller.Workspace)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return s.Reveal()
}

func newKey(t *testing.T, secret string) ticket.Key {
	t.Helper()
	k, err := ticket.NewKey([]byte(secret))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return k
}

// mint returns a ticket for the routes' session, issued at the given time.
func mint(t *testing.T, k ticket.Key, at time.Time) string {
	t.Helper()
	ticket, err := k.MintTicket(ticketCaller, at, at)
	if err != nil {
		t.Fatalf("MintTicket: %v", err)
	}
	return ticket.Reveal()
}

func (f *fixture) send(method, path, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	f.rt.ServeHTTP(rec, req)
	return rec
}

// assertProblem checks rec is a problem no client tells apart from others with its status.
func assertProblem(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	var p struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
	}
	err := json.NewDecoder(rec.Body).Decode(&p)
	if rec.Code != status || rec.Header().Get("Content-Type") != "application/problem+json" || err != nil ||
		p.Type != "about:blank" || p.Title != http.StatusText(status) || p.Status != status {
		t.Errorf("%d %s %+v (%v), want an about:blank problem with status %d and its title", rec.Code, rec.Header().Get("Content-Type"), p, err, status)
	}
}

func TestEveryRouteRefusesARequestWithoutAValidCredential(t *testing.T) {
	f := newFixture(t)
	valid := mint(t, f.key, now)
	// The user's ID swapped for another's in the claims, the signature left as it was.
	parts := strings.Split(valid, ".")
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode the claims: %v", err)
	}
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(string(claims), caller.User.String(), uuid.NewV7().String(), 1)))
	edited := strings.Join(parts, ".")
	missing, invalid := "Bearer", `Bearer error="invalid_token"` // as RFC 6750 asks
	credentials := []struct{ name, authorization, challenge string }{
		{"no credential", "", missing},
		{"another scheme", "Basic " + valid, missing},
		{"a bearer with nothing after it", "Bearer ", missing},
		{"not a ticket", "Bearer abc", invalid},
		{"issued in the future", "Bearer " + mint(t, f.key, now.Add(time.Hour)), invalid},
		{"signed with another key", "Bearer " + mint(t, newKey(t, "fedcba9876543210fedcba9876543210"), now), invalid},
		{"edited after signing", "Bearer " + edited, invalid},
	}
	for _, r := range routes {
		for _, cr := range credentials {
			t.Run(r.pattern+", "+cr.name, func(t *testing.T) {
				method, _, _ := strings.Cut(r.pattern, " ")
				rec := f.send(method, r.path, cr.authorization)
				assertProblem(t, rec, http.StatusUnauthorized)
				if got := rec.Header().Get("WWW-Authenticate"); got != cr.challenge {
					t.Errorf("WWW-Authenticate = %q, want %q", got, cr.challenge)
				}
				if _, ok := f.reached[r.pattern]; ok {
					t.Error("the route's handler ran")
				}
			})
		}
		method, _, _ := strings.Cut(r.pattern, " ")
		// What each route takes: a session's route its ticket, a product's route its key.
		credential, want := valid, holder(ticketCaller)
		if !r.tickets {
			credential, want = f.productKey(t), access.Caller{Workspace: caller.Workspace, Product: true}
		}
		if f.send(method, r.path, "Bearer "+credential); f.reached[r.pattern] != want {
			t.Errorf("%s with a valid credential reached its handler with %+v, want %+v", r.pattern, f.reached[r.pattern], want)
		}
	}
}

func TestTheBearerSchemeIsCaseInsensitive(t *testing.T) {
	f := newFixture(t)
	path := "/v1/sessions/" + session + "/turns"
	if rec := f.send("POST", path, "bearer "+mint(t, f.key, now)); rec.Code != http.StatusOK || f.reached["POST /v1/sessions/{id}/turns"] != holder(ticketCaller) {
		t.Errorf("status = %d, reached %+v; want 200 and the ticket's caller", rec.Code, f.reached)
	}
}

func TestAnUnknownPathIsAProblem(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/v1/nothing", "/v1//nothing", "//nothing"} {
		assertProblem(t, f.send("GET", path, ""), http.StatusNotFound)
	}
}

func TestAProductsRoutesTakeItsKeyAsItsWorkspace(t *testing.T) {
	f := newFixture(t)
	key := f.productKey(t)
	for _, r := range routes {
		if !r.keys {
			continue
		}
		method, _, _ := strings.Cut(r.pattern, " ")
		if rec := f.send(method, r.path, "Bearer "+key); rec.Code != http.StatusOK || f.reached[r.pattern] != (access.Caller{Workspace: caller.Workspace, Product: true}) {
			t.Errorf("%s: %d, reached with %+v; want a product in the key's workspace, with no user", r.pattern, rec.Code, f.reached[r.pattern])
		}
	}
}

func TestAKeyThatDoesntWorkIsRefusedAndNeverLogged(t *testing.T) {
	f := newFixture(t)
	revoked, k, err := f.keys.Create(context.Background(), caller.Workspace)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, err := f.keys.Revoke(context.Background(), caller.Workspace, k.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	for name, key := range map[string]string{"revoked": revoked.Reveal(), "never made": "vgk_" + strings.Repeat("A", 43)} {
		rec := f.send("POST", "/v1/sessions", "Bearer "+key)
		assertProblem(t, rec, http.StatusUnauthorized)
		if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer error="invalid_token"` {
			t.Errorf("%s: WWW-Authenticate = %q", name, got)
		}
		if strings.Contains(f.logs.String(), key) {
			t.Errorf("%s: the key is in the log", name)
		}
	}
	if len(f.reached) != 0 {
		t.Errorf("handlers ran: %v", f.reached)
	}
}

func TestAKeyThatCantBeCheckedIsAnsweredUnavailableNotRefused(t *testing.T) {
	f := newFixture(t)
	key := f.productKey(t)
	f.store.Fail(errors.New("database down"))
	rec := f.send("POST", "/v1/sessions", "Bearer "+key)
	assertProblem(t, rec, http.StatusServiceUnavailable)
	if len(f.reached) != 0 || strings.Contains(f.logs.String(), key) {
		t.Errorf("reached %v, logs %q; want no handler run and no key logged", f.reached, f.logs.String())
	}
}

// The session's end is the one route in a session that takes a key, so the
// product's server can end a session whose ticket was stolen.
func TestARouteTheBrowserCallsRefusesAProductKeyUnread(t *testing.T) {
	f := newFixture(t)
	key := f.productKey(t)
	for _, r := range routes {
		if r.keys {
			continue
		}
		method, _, _ := strings.Cut(r.pattern, " ")
		assertProblem(t, f.send(method, r.path, "Bearer "+key), http.StatusUnauthorized)
	}
	if len(f.reached) != 0 || f.store.Reads() != 0 {
		t.Errorf("reached %v after %d key reads; want no handler run and no key read", f.reached, f.store.Reads())
	}
}

func TestARouterWithoutProductKeysRefusesThem(t *testing.T) {
	rt := api.NewRouter(newKey(t, "0123456789abcdef0123456789abcdef"), nil, func() time.Time { return now }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rt.HandleProduct("GET /v1/agents", api.RequestTimeout, func(http.ResponseWriter, *http.Request, access.Caller) { t.Error("the handler ran") })
	req := httptest.NewRequest("GET", "/v1/agents", nil)
	req.Header.Set("Authorization", "Bearer vgk_"+strings.Repeat("A", 43))
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	assertProblem(t, rec, http.StatusUnauthorized)
}

func TestAWrongMethodIsAProblem(t *testing.T) {
	rec := newFixture(t).send("DELETE", "/v1/agents", "")
	assertProblem(t, rec, http.StatusMethodNotAllowed)
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "GET") {
		t.Errorf("Allow = %q, want GET listed", allow)
	}
}
