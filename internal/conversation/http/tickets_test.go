package http_test

import (
	"context"
	"io"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
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

// productFixture serves conversation's routes as one instance of voice, on
// stores and a signing key other instances may share, with a clock the test
// moves; the product's server starts sessions with its key.
type productFixture struct {
	fixture
	versions agents.Versions
	sessions *sessionsfake.Sessions
	turns    *sessionsfake.Turns
	items    usercontext.Items
	svc      *conversation.Service
	at       time.Time
	product  string // a working product key
}

type startReply struct {
	sessionReply
	Ticket string `json:"ticket"`
}

const idle = 5 * time.Minute

func newProductFixture(t *testing.T) *productFixture {
	t.Helper()
	key, err := ticket.NewKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	f := &productFixture{versions: newFixtureStore(t), sessions: sessionsfake.New(), items: itemsfake.New(), at: now}
	f.turns = sessionsfake.NewTurnsFor(f.sessions)
	f.sessions.SetClock(func() time.Time { return f.at })
	f.keys = productkey.NewKeys(keysfake.New())
	f.key = key
	f.handler, f.svc = f.instance()
	f.product = f.productKey(t)
	return f
}

// instance returns an instance of voice: its own router and service, sharing
// only the stores, the product keys and the signing key with the others.
func (f *productFixture) instance() (nethttp.Handler, *conversation.Service) {
	svc := conversation.NewService(agents.NewCatalog(f.versions), f.sessions, f.turns, pieces("Bearing 040."), usercontext.NewService(f.items),
		conversation.Settings{UserContextChars: 1000, SessionContextChars: 1000, ScreenInputChars: 1000, WordsChars: 100, TurnDeadline: time.Minute, Model: "claude-test", SessionIdle: idle})
	rt := api.NewRouter(f.key, f.keys, func() time.Time { return f.at }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	conversationhttp.Register(rt, svc)
	return rt, svc
}

// start starts a session for the user the product names, as its server does.
func (f *productFixture) start(t *testing.T, reference string, request uuid.UUID) (*httptest.ResponseRecorder, startReply) {
	t.Helper()
	rec := f.send("POST", "/v1/sessions", f.product, `{"agent_id":"`+keeperID.String()+`","request_id":"`+request.String()+`","user":"`+reference+`"}`)
	if rec.Code != nethttp.StatusCreated && rec.Code != nethttp.StatusOK {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	return rec, decode[startReply](t, rec)
}

func (f *productFixture) turn(session, ticket string) *httptest.ResponseRecorder {
	return f.send("POST", "/v1/sessions/"+session+"/turns", ticket, turnBody("Is the lamp lit?"))
}

func TestAProductsStartAnswersWithATicketTheBrowserTalksAndEndsWith(t *testing.T) {
	f := newProductFixture(t)
	rec, started := f.start(t, "aiko-42", uuid.NewV7())
	if started.Ticket == "" || started.ID == "" {
		t.Fatalf("start = %+v, want the session and a ticket", started)
	}
	// It carries a credential, so nothing along the way may keep it.
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if rec := f.turn(started.ID, started.Ticket); rec.Code != nethttp.StatusOK || !strings.Contains(rec.Body.String(), "event: done") {
		t.Errorf("turn with the ticket: %d %s, want the reply", rec.Code, rec.Body)
	}
	if rec := f.do("POST", "/v1/sessions/"+started.ID+"/end", started.Ticket); rec.Code != nethttp.StatusOK || decode[sessionReply](t, rec).EndedAt == nil {
		t.Errorf("end with the ticket: %d, want 200 with the session ended", rec.Code)
	}
}

// A start whose reply was lost is sent again with its request ID; the product
// gets the session it started, with a ticket it can hand the browser.
func TestARepeatStartGetsTheSameSessionAndAFreshTicket(t *testing.T) {
	f := newProductFixture(t)
	request := uuid.NewV7()
	_, lost := f.start(t, "aiko-42", request)
	f.at = now.Add(time.Second)
	rec, again := f.start(t, "aiko-42", request)
	if rec.Code != nethttp.StatusOK || again.sessionReply != lost.sessionReply || again.Ticket == lost.Ticket {
		t.Fatalf("repeat = %d %+v, want 200 with the same session and a new ticket", rec.Code, again)
	}
	if rec := f.turn(again.ID, again.Ticket); rec.Code != nethttp.StatusOK {
		t.Errorf("turn with the repeat's ticket: %d %s, want 200", rec.Code, rec.Body)
	}
	// A repeat never stretches the session's tickets: one hours later expires
	// with the first, four hours after the session started.
	f.at = now.Add(3 * time.Hour)
	_, late := f.start(t, "aiko-42", request)
	f.at = now.Add(4*time.Hour + 6*time.Second) // past the cap and the leeway for clocks apart
	assertProblemType(t, f.turn(late.ID, late.Ticket), nethttp.StatusUnauthorized, "/v1/problems/ticket-expired")
}

// Past the session's four hours a repeat of its start gets the session as it
// is and no ticket: none would work, so the product starts a new session.
func TestARepeatStartPastTheSessionsFourHoursGetsNoTicket(t *testing.T) {
	f := newProductFixture(t)
	request := uuid.NewV7()
	_, first := f.start(t, "aiko-42", request)
	f.at = now.Add(4 * time.Hour)
	rec, again := f.start(t, "aiko-42", request)
	if rec.Code != nethttp.StatusOK || again.sessionReply != first.sessionReply || again.Ticket != "" {
		t.Errorf("repeat = %d %+v, want 200 with the same session and no ticket", rec.Code, again)
	}
}

func TestATicketFromOneInstanceWorksOnAnother(t *testing.T) {
	f := newProductFixture(t)
	_, started := f.start(t, "aiko-42", uuid.NewV7())
	handler, _ := f.instance()
	other := fixture{handler: handler}
	if rec := other.send("POST", "/v1/sessions/"+started.ID+"/turns", started.Ticket, turnBody("Is the lamp lit?")); rec.Code != nethttp.StatusOK {
		t.Errorf("turn on another instance: %d %s, want 200", rec.Code, rec.Body)
	}
}

// Only voice-guide signs tickets, so one naming another workspace than its
// session's is a minting mistake; the session is read within the ticket's
// workspace, so it finds nothing there.
func TestATicketNamingAnotherWorkspaceFindsNoSession(t *testing.T) {
	f := newProductFixture(t)
	_, started := f.start(t, "aiko-42", uuid.NewV7())
	user, found, err := f.sessions.UserByReference(context.Background(), workspace, "aiko-42")
	if err != nil || !found {
		t.Fatalf("UserByReference: %v, found %v", err, found)
	}
	other := uuid.MustParse("01a1de8a-0000-7000-8000-000000000002")
	ticket, err := f.key.MintTicket(access.Caller{Workspace: other, User: user, Session: uuid.MustParse(started.ID)}, now, now)
	if err != nil {
		t.Fatalf("MintTicket: %v", err)
	}
	assertProblem(t, f.turn(started.ID, ticket.Reveal()), nethttp.StatusNotFound)
}

func TestRevokingTheKeyStopsStartsButNotSessionsAlreadyStarted(t *testing.T) {
	f := newProductFixture(t)
	_, started := f.start(t, "aiko-42", uuid.NewV7())
	list, err := f.keys.List(context.Background(), workspace)
	if err != nil || len(list) != 1 {
		t.Fatalf("List: %v, %d keys", err, len(list))
	}
	if _, _, err := f.keys.Revoke(context.Background(), workspace, list[0].ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	body := `{"agent_id":"` + keeperID.String() + `","request_id":"` + uuid.NewV7().String() + `","user":"aiko-42"}`
	assertProblem(t, f.send("POST", "/v1/sessions", f.product, body), nethttp.StatusUnauthorized)
	if rec := f.turn(started.ID, started.Ticket); rec.Code != nethttp.StatusOK {
		t.Errorf("turn after the revoke: %d %s, want 200", rec.Code, rec.Body)
	}
}

// A ticket works while its session is live: once it has ended, by its End or
// by sitting idle, its turns are refused as in an ended session, and ending it
// again answers as the first end did.
func TestATicketOutlivingItsSessionTakesNoTurns(t *testing.T) {
	ends := map[string]func(*productFixture, startReply){
		"ended with the ticket": func(f *productFixture, s startReply) {
			f.do("POST", "/v1/sessions/"+s.ID+"/end", s.Ticket)
		},
		"ended by the idle sweep": func(f *productFixture, _ startReply) {
			f.at = now.Add(idle)
			if n, err := f.svc.EndIdle(context.Background()); err != nil || n != 1 {
				t.Fatalf("EndIdle = %d, %v; want the session ended", n, err)
			}
		},
	}
	for name, end := range ends {
		t.Run(name, func(t *testing.T) {
			f := newProductFixture(t)
			_, started := f.start(t, "aiko-42", uuid.NewV7())
			end(f, started)
			assertProblemType(t, f.turn(started.ID, started.Ticket), nethttp.StatusConflict, "/v1/problems/session-ended")
			if rec := f.do("POST", "/v1/sessions/"+started.ID+"/end", started.Ticket); rec.Code != nethttp.StatusOK || decode[sessionReply](t, rec).EndedAt == nil {
				t.Errorf("end again: %d, want 200 with the session ended", rec.Code)
			}
		})
	}
}

// The product's server ends any session of its workspace with its key, the
// way to stop a session whose ticket was stolen without rotating
// voice-guide's signing key; the stolen ticket then takes no turns.
func TestAProductsKeyEndsAnySessionOfItsWorkspace(t *testing.T) {
	f := newProductFixture(t)
	_, started := f.start(t, "aiko-42", uuid.NewV7())
	elsewhere, _, err := f.keys.Create(context.Background(), uuid.MustParse("01a1de8a-0000-7000-8000-000000000002"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	assertProblem(t, f.do("POST", "/v1/sessions/"+started.ID+"/end", elsewhere.Reveal()), nethttp.StatusNotFound)
	rec := f.do("POST", "/v1/sessions/"+started.ID+"/end", f.product)
	if ended := decode[sessionReply](t, rec); rec.Code != nethttp.StatusOK || ended.ID != started.ID || ended.EndedAt == nil {
		t.Fatalf("end with the key: %d %+v, want 200 with the session ended", rec.Code, ended)
	}
	assertProblemType(t, f.turn(started.ID, started.Ticket), nethttp.StatusConflict, "/v1/problems/session-ended")
}

// End is the one thing a ticket does past its lifetime, so a user who stayed
// on can still end the session; that turns refuse it then is the router's
// check, tested there.
func TestEndTakesTheSessionsTicketPastItsLifetime(t *testing.T) {
	f := newProductFixture(t)
	_, started := f.start(t, "aiko-42", uuid.NewV7())
	f.at = now.Add(4*time.Hour + 10*time.Minute)
	rec := f.do("POST", "/v1/sessions/"+started.ID+"/end", started.Ticket)
	if ended := decode[sessionReply](t, rec); rec.Code != nethttp.StatusOK || ended.EndedAt == nil {
		t.Errorf("end past the ticket's lifetime: %d %+v, want 200 with the session ended", rec.Code, ended)
	}
}
