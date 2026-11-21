package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/api"
	"github.com/mikaclements/voice-guide/internal/conversation"
	sessionsfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/productkey"
	keysfake "github.com/mikaclements/voice-guide/internal/productkey/store/fake"
	"github.com/mikaclements/voice-guide/internal/speech"
	detectionfake "github.com/mikaclements/voice-guide/internal/speech/detection/fake"
	speechhttp "github.com/mikaclements/voice-guide/internal/speech/http"
	"github.com/mikaclements/voice-guide/internal/speech/transport/fake"
	"github.com/mikaclements/voice-guide/internal/ticket"
)

var (
	// The real time, whole seconds as a ticket keeps it: speech ends a
	// connection when the ticket that opened it expires, by its own clock.
	now       = time.Now().UTC().Truncate(time.Second)
	workspace = uuid.MustParse("01a1de8a-0000-7000-8000-000000000001")
	user      = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a1")
	caller    = access.Caller{Workspace: workspace, User: user}
)

type fixture struct {
	handler   nethttp.Handler
	key       ticket.Key
	audio     []string // each session's audio path
	transport *fake.Transport
	svc       *speech.Service
	logs      *bytes.Buffer
	sessions  *sessionsfake.Sessions
	keys      *productkey.Keys
}

// newFixture serves the audio endpoint over the fake transport, holding at
// most max connections, with two sessions of the caller's.
func newFixture(t *testing.T, max int) fixture {
	t.Helper()
	sessions := sessionsfake.New()
	var audio []string
	for range 2 {
		s, _, err := sessions.Start(context.Background(), conversation.Session{ID: uuid.NewV7(), Workspace: workspace, User: user, Agent: uuid.NewV7(), AgentVersion: 1}, uuid.NewV7(), "")
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		audio = append(audio, "/v1/sessions/"+s.ID.String()+"/audio")
	}
	transport := fake.New()
	svc := speech.NewService(conversation.NewService(nil, sessions, nil, nil, nil, conversation.Settings{}), transport, nil, detectionfake.Detection{}, nil, nil, speech.Settings{MaxConnections: max}, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { closeService(t, svc) })
	key, err := ticket.NewKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	logs := &bytes.Buffer{}
	keys := productkey.NewKeys(keysfake.New())
	rt := api.NewRouter(key, keys, func() time.Time { return now }, slog.New(slog.NewTextHandler(logs, nil)))
	speechhttp.Register(rt, svc)
	return fixture{rt, key, audio, transport, svc, logs, sessions, keys}
}

// closeService closes the service, failing within 5s rather than hanging when
// Close waits for a connection nothing ended: the fake ends one in
// microseconds.
func closeService(t *testing.T, svc *speech.Service) {
	t.Helper()
	closed := make(chan struct{})
	go func() { svc.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Error("Close didn't return: it waits for a connection nothing ended")
	}
}

// bearer returns a ticket as the session's start hands it back, for the
// session the path names, which may not exist, and for the user c names, who
// may not be the session's own.
func (f fixture) bearer(t *testing.T, c access.Caller, path string) string {
	t.Helper()
	c.Session = uuid.MustParse(strings.Split(strings.TrimPrefix(path, "/v1/sessions/"), "/")[0])
	ticket, err := f.key.MintTicket(c, now, now)
	if err != nil {
		t.Fatalf("MintTicket: %v", err)
	}
	return ticket.Reveal()
}

func (f fixture) send(t *testing.T, method string, c *access.Caller, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	bearer := ""
	if c != nil {
		bearer = f.bearer(t, *c, path)
	}
	return f.sendWith(context.Background(), method, bearer, path, "application/sdp", body)
}

func (f fixture) sendWith(ctx context.Context, method, bearer, path, contentType, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	r.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

// connect posts an offer for the session's audio and returns the connection's address.
func (f fixture) connect(t *testing.T, audio string) string {
	t.Helper()
	w := f.send(t, "POST", &caller, audio, "offer")
	location := w.Header().Get("Location")
	id, found := strings.CutPrefix(location, audio+"/")
	if _, err := uuid.Parse(id); w.Code != nethttp.StatusCreated || !found || err != nil || w.Header().Get("Content-Type") != "application/sdp" || w.Body.String() != "answer to offer" {
		t.Fatalf("POST = %d %q Location %q, %q; want 201 application/sdp at %s/ID with the answer", w.Code, w.Header().Get("Content-Type"), location, w.Body, audio)
	}
	return location
}

func TestDeleteEndsTheConnectionAtItsAddress(t *testing.T) {
	f := newFixture(t, 10)
	conn := f.connect(t, f.audio[0])
	if w := f.send(t, "DELETE", &caller, conn, ""); w.Code != nethttp.StatusOK {
		t.Fatalf("DELETE = %d %s, want 200", w.Code, w.Body)
	}
	if got := f.transport.Open(); got != 0 {
		t.Errorf("%d connections open after DELETE, want 0", got)
	}
	if w := f.send(t, "DELETE", &caller, conn, ""); w.Code != nethttp.StatusNotFound {
		t.Errorf("second DELETE = %d, want 404: nothing left to end", w.Code)
	}
}

func TestALateDeleteFromAReplacedTabLeavesItsSuccessor(t *testing.T) {
	f := newFixture(t, 10)
	first := f.connect(t, f.audio[0])
	second := f.connect(t, f.audio[0])
	if w := f.send(t, "DELETE", &caller, first, ""); w.Code != nethttp.StatusNotFound {
		t.Errorf("DELETE of the replaced connection = %d, want 404", w.Code)
	}
	if w := f.send(t, "DELETE", &caller, second, ""); w.Code != nethttp.StatusOK {
		t.Errorf("DELETE of the newer connection = %d, want 200: the late DELETE left it open", w.Code)
	}
}

func TestRefusalsAreProblemsAndConnectNothing(t *testing.T) {
	f := newFixture(t, 10)
	stranger := access.Caller{Workspace: workspace, User: uuid.NewV7()}
	audio := f.audio[0]
	conn := audio + "/" + uuid.NewV7().String()
	tests := map[string]struct {
		method      string
		caller      *access.Caller
		path        string
		contentType string
		body        string
		status      int
	}{
		"another user's session":          {"POST", &stranger, audio, "application/sdp", "offer", nethttp.StatusNotFound},
		"a session that doesn't exist":    {"POST", &caller, "/v1/sessions/" + uuid.NewV7().String() + "/audio", "application/sdp", "offer", nethttp.StatusNotFound},
		"an offer that isn't SDP":         {"POST", &caller, audio, "application/json", "offer", nethttp.StatusUnsupportedMediaType},
		"an offer that can't be answered": {"POST", &caller, audio, "application/sdp", fake.BadOffer, nethttp.StatusBadRequest},
		"an offer over the size limit":    {"POST", &caller, audio, "application/sdp", strings.Repeat("a", 65<<10), nethttp.StatusRequestEntityTooLarge},
		"GET on the session's audio":      {"GET", &caller, audio, "", "", nethttp.StatusMethodNotAllowed},
		"an offer to a connection":        {"POST", &caller, conn, "application/sdp", "offer", nethttp.StatusMethodNotAllowed},
		"DELETE with nothing to end":      {"DELETE", &caller, conn, "", "", nethttp.StatusNotFound},
		"DELETE of an ID that isn't one":  {"DELETE", &caller, audio + "/lamp", "", "", nethttp.StatusNotFound},
		"DELETE of another's session":     {"DELETE", &stranger, conn, "", "", nethttp.StatusNotFound},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			bearer := ""
			if tt.caller != nil {
				bearer = f.bearer(t, *tt.caller, tt.path)
			}
			w := f.sendWith(context.Background(), tt.method, bearer, tt.path, tt.contentType, tt.body)
			if w.Code != tt.status || w.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("%s = %d %q, want %d application/problem+json", tt.method, w.Code, w.Header().Get("Content-Type"), tt.status)
			}
			var problem struct {
				Type   string `json:"type"`
				Status int    `json:"status"`
			}
			if err := json.NewDecoder(w.Body).Decode(&problem); err != nil || problem.Status != tt.status || problem.Type != "about:blank" {
				t.Errorf("problem = %+v, %v; want about:blank with status %d", problem, err, tt.status)
			}
		})
	}
	if got := f.transport.Answered(); got != 0 {
		t.Errorf("the transport answered %d offers, want none", got)
	}
	if w := f.send(t, "GET", &caller, audio, ""); w.Header().Get("Allow") != "POST" {
		t.Errorf("Allow = %q, want POST", w.Header().Get("Allow"))
	}
}

func TestEveryAudioRouteNeedsATicket(t *testing.T) {
	f := newFixture(t, 10)
	for method, path := range map[string]string{"POST": f.audio[0], "DELETE": f.audio[0] + "/" + uuid.NewV7().String()} {
		if w := f.send(t, method, nil, path, "offer"); w.Code != nethttp.StatusUnauthorized {
			t.Errorf("%s with no ticket = %d, want 401", method, w.Code)
		}
	}
	if got := f.transport.Answered(); got != 0 {
		t.Errorf("the transport answered %d offers, want none", got)
	}
}

func TestEveryAudioRouteRefusesAProductKey(t *testing.T) {
	f := newFixture(t, 10)
	key, _, err := f.keys.Create(context.Background(), workspace, access.RunSessions)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for method, path := range map[string]string{"POST": f.audio[0], "DELETE": f.audio[0] + "/" + uuid.NewV7().String()} {
		if w := f.sendWith(context.Background(), method, key.Reveal(), path, "application/sdp", "offer"); w.Code != nethttp.StatusUnauthorized {
			t.Errorf("%s with the workspace's product key = %d, want 401", method, w.Code)
		}
	}
	if got := f.transport.Answered(); got != 0 {
		t.Errorf("the transport answered %d offers, want none", got)
	}
}

func TestAFullServerAsksTheBrowserToRetry(t *testing.T) {
	f := newFixture(t, 1)
	f.connect(t, f.audio[0])
	w := f.send(t, "POST", &caller, f.audio[1], "offer")
	if w.Code != nethttp.StatusServiceUnavailable || w.Header().Get("Retry-After") != "5" {
		t.Errorf("POST to a full server = %d, Retry-After %q; want 503 with 5", w.Code, w.Header().Get("Retry-After"))
	}
}

func TestAnOfferWhileTheServerStopsIsRefusedForAnotherServer(t *testing.T) {
	f := newFixture(t, 10)
	f.transport.Hold = make(chan struct{})
	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- f.send(t, "POST", &caller, f.audio[0], "offer") }()
	f.transport.WaitHeld()
	closeService(t, f.svc) // the server stops while the offer is answered
	close(f.transport.Hold)
	if w := <-done; w.Code != nethttp.StatusServiceUnavailable {
		t.Errorf("POST while stopping = %d, want 503 so the browser sends it again", w.Code)
	}
}

func TestAnOfferCutShortIsRetriedNotLoggedAsAFailure(t *testing.T) {
	f := newFixture(t, 10)
	f.transport.Hold = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *httptest.ResponseRecorder)
	go func() {
		done <- f.sendWith(ctx, "POST", f.bearer(t, caller, f.audio[0]), f.audio[0], "application/sdp", "offer")
	}()
	f.transport.WaitHeld()
	cancel() // the server stopping cancels the requests in flight
	if w := <-done; w.Code != nethttp.StatusServiceUnavailable {
		t.Errorf("POST cut short = %d, want 503", w.Code)
	}
	if logs := f.logs.String(); strings.Contains(logs, "level=ERROR") || !strings.Contains(logs, "level=WARN") {
		t.Errorf("logs = %q, want a warning and no error", logs)
	}
}

func TestAnOfferForAnEndedSessionIsRefusedWithTheSessionEndedProblem(t *testing.T) {
	f := newFixture(t, 10)
	id := uuid.MustParse(strings.TrimSuffix(strings.TrimPrefix(f.audio[0], "/v1/sessions/"), "/audio"))
	if _, found, err := f.sessions.End(context.Background(), workspace, user, id); err != nil || !found {
		t.Fatalf("End = %v, %v", found, err)
	}
	w := f.send(t, "POST", &caller, f.audio[0], "offer")
	var problem struct {
		Type   string `json:"type"`
		Status int    `json:"status"`
	}
	if err := json.NewDecoder(w.Body).Decode(&problem); err != nil || w.Code != nethttp.StatusConflict || problem.Type != "/v1/problems/session-ended" {
		t.Errorf("POST = %d %+v, %v; want 409 of type /v1/problems/session-ended", w.Code, problem, err)
	}
	if got := f.transport.Answered(); got != 0 {
		t.Errorf("the transport answered %d offers, want none", got)
	}
}
