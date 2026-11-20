package speech_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/conversation"
	sessionsfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/speech"
	detectionfake "github.com/mikaclements/voice-guide/internal/speech/detection/fake"
	"github.com/mikaclements/voice-guide/internal/speech/transport/fake"
)

var (
	workspace = uuid.MustParse("01a1de8a-0000-7000-8000-000000000001")
	user      = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a1")
	keeperID  = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000b1")
	// openerID is an agent that speaks first in its sessions.
	openerID = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000b2")
	// expires is when the callers' tickets expire, as every caller on speech's
	// routes holds one: an hour after the tests start, by the system clock the
	// service reads unless a test sets one, so no test outlasts it.
	expires = time.Now().Add(time.Hour)
	caller  = access.Caller{Workspace: workspace, User: user, Expires: expires}
	// product is the workspace's product's server, calling with its key.
	product = access.Caller{Workspace: workspace, Product: true}
)

// wait bounds how long a test waits for something the fake does in
// microseconds, so 5s means it never will; the test fails instead of hanging.
const wait = 5 * time.Second

// newService returns the service over the fake transport, holding at most
// max connections, with sessions of the caller's.
func newService(t *testing.T, max, sessions int) (*speech.Service, *fake.Transport, []uuid.UUID) {
	t.Helper()
	transport := fake.New()
	svc, ids := newServiceOver(t, transport, nil, max, sessions)
	return svc, transport, ids
}

// newServiceOver returns the service over the given transport, reading
// sessions through wrap's store when wrap is set.
func newServiceOver(t *testing.T, transport speech.Transport, wrap func(conversation.Sessions) conversation.Sessions, max, sessions int) (*speech.Service, []uuid.UUID) {
	t.Helper()
	store := sessionsfake.New()
	var ids []uuid.UUID
	for range sessions {
		s, _, err := store.Start(context.Background(), conversation.Session{
			ID: uuid.NewV7(), Workspace: workspace, User: user, Agent: keeperID, AgentVersion: 1,
		}, uuid.NewV7(), "")
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		ids = append(ids, s.ID)
	}
	var read conversation.Sessions = store
	if wrap != nil {
		read = wrap(store)
	}
	svc := speech.NewService(conversation.NewService(nil, read, nil, nil, nil, conversation.Settings{}), transport, nil, detectionfake.Detection{}, nil, nil, speech.Settings{MaxConnections: max}, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { closeService(t, svc) })
	return svc, ids
}

// closeService closes the service, failing rather than hanging when Close
// waits for a connection nothing ended.
func closeService(t *testing.T, svc *speech.Service) {
	t.Helper()
	closed := make(chan struct{})
	go func() { svc.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(wait):
		t.Fatal("Close didn't return: it waits for a connection nothing ended")
	}
}

// waitClosed fails the test unless done is closed within the wait.
func waitClosed(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatalf("waited %s for %s", wait, what)
	}
}

// waitLetGo waits until served is closed, then fails the test unless the
// server holds no connection: a connection it let go gives its place back.
func waitLetGo(t *testing.T, svc *speech.Service, what string, served <-chan struct{}) {
	t.Helper()
	waitClosed(t, what, served)
	if got := svc.Held(); got != 0 {
		t.Errorf("held %d once %s, want 0", got, what)
	}
}

func connect(t *testing.T, svc *speech.Service, session uuid.UUID) uuid.UUID {
	t.Helper()
	id, answer, err := svc.Connect(context.Background(), caller, session, "offer")
	// A UUID's version is the high half of its seventh byte.
	if err != nil || answer != "answer to offer" || id[6]>>4 != 7 {
		t.Fatalf("Connect = %v, %q, %v; want a UUIDv7 and the transport's answer", id, answer, err)
	}
	return id
}

func TestConnectAnswersTheOfferForTheCallersSession(t *testing.T) {
	svc, transport, sessions := newService(t, 10, 1)
	connect(t, svc, sessions[0])
	if transport.Open() != 1 || svc.Held() != 1 {
		t.Errorf("%d open, %d held; want 1 each", transport.Open(), svc.Held())
	}
}

func TestConnectRefusesSessionsOutOfTheCallersReachWithoutAnswering(t *testing.T) {
	svc, transport, sessions := newService(t, 10, 1)
	id := connect(t, svc, sessions[0])
	tests := map[string]struct {
		caller  access.Caller
		session uuid.UUID
	}{
		"a session that doesn't exist": {caller, uuid.NewV7()},
		"another user":                 {access.Caller{Workspace: workspace, User: uuid.NewV7(), Expires: expires}, sessions[0]},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := svc.Connect(context.Background(), tt.caller, tt.session, "offer"); !errors.Is(err, conversation.ErrNoSuchSession) {
				t.Errorf("Connect = %v, want ErrNoSuchSession", err)
			}
			if err := svc.Hangup(context.Background(), tt.caller, tt.session, id); !errors.Is(err, conversation.ErrNoSuchSession) {
				t.Errorf("Hangup = %v, want ErrNoSuchSession", err)
			}
		})
	}
	if transport.Answered() != 1 || transport.Open() != 1 {
		t.Errorf("answered %d, %d open; want only the caller's own connection", transport.Answered(), transport.Open())
	}
}

func TestAnOfferTheTransportCantAnswerIsRefused(t *testing.T) {
	svc, _, sessions := newService(t, 10, 1)
	if _, _, err := svc.Connect(context.Background(), caller, sessions[0], fake.BadOffer); !errors.Is(err, speech.ErrBadOffer) {
		t.Errorf("Connect = %v, want ErrBadOffer", err)
	}
}

func TestANewConnectionReplacesTheOldOneWhichIsToldSo(t *testing.T) {
	svc, transport, sessions := newService(t, 10, 1)
	first := connect(t, svc, sessions[0])
	second := connect(t, svc, sessions[0])
	select {
	case <-transport.Conn(0).Done():
	case <-time.After(wait):
		t.Fatalf("waited %s for the first connection to close", wait)
	}
	if got := transport.Reasons(); !slices.Equal(got, []speech.CloseReason{speech.Replaced, ""}) {
		t.Errorf("reasons = %q, want the first replaced and the second open", got)
	}
	// The replaced tab's own hang-up arrives late: it must not end its successor.
	if err := svc.Hangup(context.Background(), caller, sessions[0], first); !errors.Is(err, speech.ErrNoConnection) {
		t.Errorf("Hangup(first) = %v, want ErrNoConnection", err)
	}
	if err := svc.Hangup(context.Background(), caller, sessions[0], second); err != nil {
		t.Errorf("Hangup(second) = %v, want it ended", err)
	}
	if got := transport.Reasons()[1]; got != speech.Ended {
		t.Errorf("the second closed for %q, want ended", got)
	}
}

func TestHangupEndsTheConnectionOnce(t *testing.T) {
	svc, transport, sessions := newService(t, 10, 1)
	id := connect(t, svc, sessions[0])
	if err := svc.Hangup(context.Background(), caller, sessions[0], uuid.NewV7()); !errors.Is(err, speech.ErrNoConnection) {
		t.Errorf("Hangup(another ID) = %v, want ErrNoConnection", err)
	}
	if err := svc.Hangup(context.Background(), caller, sessions[0], id); err != nil {
		t.Fatalf("Hangup = %v", err)
	}
	if got := transport.Open(); got != 0 {
		t.Errorf("%d connections open, want 0", got)
	}
	if err := svc.Hangup(context.Background(), caller, sessions[0], id); !errors.Is(err, speech.ErrNoConnection) {
		t.Errorf("second Hangup = %v, want ErrNoConnection", err)
	}
}

func TestAConnectionTheBrowserEndsIsLetGo(t *testing.T) {
	svc, transport, sessions := newService(t, 10, 1)
	connect(t, svc, sessions[0])
	served := svc.Served(sessions[0])
	transport.EndAll() // the browser hangs up, or its network drops for good
	waitLetGo(t, svc, "the server let the ended connection go", served)
}

func TestAFullServerRefusesNewSessionsButNotTheirReplacements(t *testing.T) {
	svc, transport, sessions := newService(t, 1, 2)
	connect(t, svc, sessions[0])
	if _, _, err := svc.Connect(context.Background(), caller, sessions[1], "offer"); !errors.Is(err, speech.ErrFull) {
		t.Errorf("Connect(another session) = %v, want ErrFull", err)
	}
	if got := transport.Answered(); got != 1 {
		t.Errorf("the transport answered %d offers, want the refused one not answered", got)
	}
	connect(t, svc, sessions[0]) // a reload replaces, so it still fits
	served := svc.Served(sessions[0])
	transport.EndAll()
	waitLetGo(t, svc, "the browser hung up", served)
	connect(t, svc, sessions[1])
}

func TestAnOfferBeingAnsweredHoldsItsPlace(t *testing.T) {
	const offers = 20 // offers for as many sessions, all at once
	svc, transport, sessions := newService(t, 1, offers)
	transport.Hold = make(chan struct{})
	results := make(chan error, offers)
	for _, session := range sessions {
		go func() {
			_, _, err := svc.Connect(context.Background(), caller, session, "offer")
			results <- err
		}()
	}
	transport.WaitHeld() // one offer is being answered
	for range offers - 1 {
		select {
		case err := <-results:
			if !errors.Is(err, speech.ErrFull) {
				t.Errorf("Connect = %v, want ErrFull: the offer being answered holds the one place", err)
			}
		case <-time.After(wait):
			close(transport.Hold)
			t.Fatal("offers past the limit wait to be answered, as if the offer being answered held no place")
		}
	}
	close(transport.Hold)
	if err := <-results; err != nil {
		t.Errorf("the offer being answered: Connect = %v", err)
	}
	if transport.Answered() != 1 || svc.Held() != 1 {
		t.Errorf("answered %d, held %d; want only the offer that held the place", transport.Answered(), svc.Held())
	}
}

func TestARefusedOfferGivesBackItsPlace(t *testing.T) {
	svc, _, sessions := newService(t, 1, 1)
	if _, _, err := svc.Connect(context.Background(), caller, sessions[0], fake.BadOffer); !errors.Is(err, speech.ErrBadOffer) {
		t.Fatalf("Connect(bad offer) = %v, want ErrBadOffer", err)
	}
	if _, _, err := svc.Connect(context.Background(), caller, uuid.NewV7(), "offer"); !errors.Is(err, conversation.ErrNoSuchSession) {
		t.Fatalf("Connect(no session) = %v, want ErrNoSuchSession", err)
	}
	connect(t, svc, sessions[0]) // the one place is free again
}

// panicky is the fake transport, panicking on the offer "panic".
type panicky struct{ *fake.Transport }

func (p panicky) Answer(ctx context.Context, offer string) (speech.Connection, string, error) {
	if offer == "panic" {
		panic("the transport broke")
	}
	return p.Transport.Answer(ctx, offer)
}

func TestAnOfferThatPanicsGivesBackItsPlace(t *testing.T) {
	svc, sessions := newServiceOver(t, panicky{fake.New()}, nil, 1, 2)
	func() {
		defer func() { _ = recover() }() // as net/http recovers a handler's panic
		_, _, _ = svc.Connect(context.Background(), caller, sessions[0], "panic")
	}()
	connect(t, svc, sessions[1]) // the one place is free again
}

// offer sends an offer for the session in the background and returns its result.
func offer(svc *speech.Service, session uuid.UUID) <-chan error {
	result := make(chan error, 1)
	go func() {
		_, _, err := svc.Connect(context.Background(), caller, session, "offer")
		result <- err
	}()
	return result
}

// answering returns once the transport holds the offer's answer, failing the
// test if the offer is refused first.
func answering(t *testing.T, transport *fake.Transport, result <-chan error) {
	t.Helper()
	held := make(chan struct{})
	go func() { transport.WaitHeld(); close(held) }()
	select {
	case <-held:
	case err := <-result:
		close(transport.Hold)
		t.Fatalf("Connect = %v, want the offer answered", err)
	case <-time.After(wait):
		t.Fatal("the offer was neither answered nor refused")
	}
}

// A session both held and being answered is counted once, not twice.
func TestAReloadBeingAnsweredTakesNoPlace(t *testing.T) {
	svc, transport, sessions := newService(t, 2, 2)
	connect(t, svc, sessions[0])
	transport.Hold = make(chan struct{})
	reload := offer(svc, sessions[0])
	answering(t, transport, reload)
	other := offer(svc, sessions[1])
	answering(t, transport, other) // the second place is still free
	close(transport.Hold)
	for _, result := range []<-chan error{reload, other} {
		if err := <-result; err != nil {
			t.Errorf("Connect = %v, want both answered", err)
		}
	}
	if got := svc.Held(); got != 2 {
		t.Errorf("held %d, want the reload and the other session", got)
	}
}

func TestAReloadKeepsTheSessionsPlaceWhenTheOldConnectionEnds(t *testing.T) {
	svc, transport, sessions := newService(t, 1, 2)
	connect(t, svc, sessions[0])
	transport.Hold = make(chan struct{})
	reload := offer(svc, sessions[0])
	answering(t, transport, reload)
	served := svc.Served(sessions[0])
	transport.EndAll() // the old tab's connection drops while its reload is answered
	waitLetGo(t, svc, "the old connection was let go", served)
	select {
	case err := <-offer(svc, sessions[1]):
		if !errors.Is(err, speech.ErrFull) {
			t.Errorf("another session's Connect = %v, want ErrFull: the reload holds the place", err)
		}
	case <-time.After(wait):
		close(transport.Hold)
		t.Fatal("another session's offer is being answered in the place the reload holds")
	}
	close(transport.Hold)
	if err := <-reload; err != nil {
		t.Errorf("the reload: Connect = %v, want it answered", err)
	}
	if got := svc.Held(); got != 1 {
		t.Errorf("held %d, want the reload", got)
	}
}

func TestOffersForOneSessionShareOnePlace(t *testing.T) {
	svc, transport, sessions := newService(t, 2, 2)
	transport.Hold = make(chan struct{})
	first := offer(svc, sessions[0])
	answering(t, transport, first)
	other := offer(svc, sessions[1])
	answering(t, transport, other)    // both places are kept
	second := offer(svc, sessions[0]) // a second tab, or a reload before the first is answered
	answering(t, transport, second)   // the session's place is its own
	close(transport.Hold)
	for _, result := range []<-chan error{first, second, other} {
		if err := <-result; err != nil {
			t.Errorf("Connect = %v, want every offer answered", err)
		}
	}
	if got := svc.Held(); got != 2 {
		t.Errorf("held %d, want one connection per session", got)
	}
}

func TestASessionsOffersBeyondAFewAtOnceAreRefused(t *testing.T) {
	svc, transport, sessions := newService(t, 1, 1)
	connect(t, svc, sessions[0])
	transport.Hold = make(chan struct{})
	reload, tab := offer(svc, sessions[0]), offer(svc, sessions[0])
	answering(t, transport, reload)
	answering(t, transport, tab)
	select {
	case err := <-offer(svc, sessions[0]):
		if !errors.Is(err, speech.ErrFull) {
			t.Errorf("the third offer: Connect = %v, want ErrFull", err)
		}
	case <-time.After(wait):
		close(transport.Hold)
		t.Fatal("a third offer for the session is being answered beside the two")
	}
	close(transport.Hold)
	for _, result := range []<-chan error{reload, tab} {
		if err := <-result; err != nil {
			t.Errorf("Connect = %v, want both answered", err)
		}
	}
}

// slowLookup is a session store whose lookup of one session waits until
// release is closed, telling entered when it starts.
type slowLookup struct {
	conversation.Sessions
	session          uuid.UUID
	entered, release chan struct{}
}

func (s slowLookup) ByID(ctx context.Context, workspace, user, id uuid.UUID) (conversation.Session, bool, error) {
	if id == s.session {
		close(s.entered)
		<-s.release
	}
	return s.Sessions.ByID(ctx, workspace, user, id)
}

func TestAnOfferForASessionNotTheCallersHoldsNoPlace(t *testing.T) {
	madeUp, entered, release := uuid.NewV7(), make(chan struct{}), make(chan struct{})
	svc, sessions := newServiceOver(t, fake.New(), func(s conversation.Sessions) conversation.Sessions {
		return slowLookup{s, madeUp, entered, release}
	}, 1, 1)
	stranger := offer(svc, madeUp)
	select {
	case <-entered: // the database is slow to say the session doesn't exist
	case <-time.After(wait):
		t.Fatal("the made-up session was never looked up")
	}
	_, _, err := svc.Connect(context.Background(), caller, sessions[0], "offer")
	close(release)
	if err != nil {
		t.Errorf("the owner's Connect = %v, want it answered: the made-up session's offer holds no place", err)
	}
	if err := <-stranger; !errors.Is(err, conversation.ErrNoSuchSession) {
		t.Errorf("the made-up session's Connect = %v, want ErrNoSuchSession", err)
	}
}

func TestCloseEndsEveryConnectionAndRefusesNewOnes(t *testing.T) {
	svc, transport, sessions := newService(t, 10, 2)
	connect(t, svc, sessions[0])
	connect(t, svc, sessions[1])
	closeService(t, svc)
	if got := transport.Reasons(); !slices.Equal(got, []speech.CloseReason{speech.Stopping, speech.Stopping}) {
		t.Errorf("reasons = %q, want both closed as stopping", got)
	}
	if _, _, err := svc.Connect(context.Background(), caller, sessions[0], "offer"); !errors.Is(err, speech.ErrStopping) {
		t.Errorf("Connect after Close = %v, want ErrStopping", err)
	}
	if got := transport.Answered(); got != 2 {
		t.Errorf("the transport answered %d offers, want only the two before Close", got)
	}
}

func TestAConnectionAnsweredWhileTheServerStopsIsEnded(t *testing.T) {
	svc, transport, sessions := newService(t, 10, 1)
	transport.Hold = make(chan struct{})
	refused := make(chan error)
	go func() {
		_, _, err := svc.Connect(context.Background(), caller, sessions[0], "offer")
		refused <- err
	}()
	transport.WaitHeld()
	closeService(t, svc) // the server stops while the transport answers
	close(transport.Hold)
	if err := <-refused; !errors.Is(err, speech.ErrStopping) {
		t.Errorf("Connect = %v, want ErrStopping", err)
	}
	if got := transport.Open(); got != 0 {
		t.Errorf("%d connections open, want the one answered while stopping ended", got)
	}
}
