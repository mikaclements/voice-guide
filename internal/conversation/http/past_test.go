package http_test

import (
	"context"
	"strings"
	"testing"
	"time"
	"uuid"

	nethttp "net/http"

	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	itemsfake "github.com/mikaclements/voice-guide/internal/usercontext/store/fake"
)

type pastSession struct {
	ID           string     `json:"id"`
	User         string     `json:"user"`
	AgentID      string     `json:"agent_id"`
	AgentVersion int        `json:"agent_version"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at"`
	Summary      *string    `json:"summary"`
	Turns        []struct {
		Seq   int    `json:"seq"`
		Words string `json:"words"`
		Reply string `json:"reply"`
		Cut   bool   `json:"cut"`
	} `json:"turns"`
}

type pastList struct {
	Sessions []pastSession `json:"sessions"`
}

// sessionsFor starts n sessions for the user the product names, oldest first, and returns their IDs.
func (f *productFixture) sessionsFor(t *testing.T, reference string, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		_, s := f.start(t, reference, uuid.NewV7())
		ids[i] = s.ID
	}
	return ids
}

func (f *productFixture) list(t *testing.T, reference, query string) pastList {
	t.Helper()
	rec := f.send("GET", "/v1/users/"+reference+"/sessions"+query, f.product, "")
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("list %s: %d %s", query, rec.Code, rec.Body)
	}
	return decode[pastList](t, rec)
}

func idsOf(l pastList) []string {
	ids := []string{}
	for _, s := range l.Sessions {
		ids = append(ids, s.ID)
	}
	return ids
}

func sameIDs(got, want []string) bool { return strings.Join(got, ",") == strings.Join(want, ",") }

func TestAUsersSessionsAreListedNewestFirstOnePageAtATime(t *testing.T) {
	f := newProductFixture(t)
	mine := f.sessionsFor(t, "aiko-42", 5)
	f.sessionsFor(t, "ben-7", 2)
	newest := []string{mine[4], mine[3], mine[2], mine[1], mine[0]}

	if got := idsOf(f.list(t, "aiko-42", "")); !sameIDs(got, newest) {
		t.Errorf("list = %v, want %v: the user's sessions only, newest first", got, newest)
	}
	first := f.list(t, "aiko-42", "?limit=2")
	if got := idsOf(first); !sameIDs(got, newest[:2]) {
		t.Fatalf("first page = %v, want %v", got, newest[:2])
	}
	second := f.list(t, "aiko-42", "?limit=2&before="+first.Sessions[1].ID)
	if got := idsOf(second); !sameIDs(got, newest[2:4]) {
		t.Fatalf("second page = %v, want %v", got, newest[2:4])
	}
	last := f.list(t, "aiko-42", "?limit=2&before="+second.Sessions[1].ID)
	if got := idsOf(last); !sameIDs(got, newest[4:]) {
		t.Errorf("last page = %v, want %v", got, newest[4:])
	}
	if s := first.Sessions[0]; s.User != "aiko-42" || s.AgentID != keeperID.String() || s.AgentVersion != 1 || s.StartedAt.IsZero() {
		t.Errorf("session = %+v, want the user by the product's reference, with its agent and start", s)
	}
}

func TestAListTakesOneToAHundredSessionsAndAnythingElseIsRefused(t *testing.T) {
	f := newProductFixture(t)
	f.sessionsFor(t, "aiko-42", 3)
	for _, limit := range []string{"1", "100"} {
		if rec := f.send("GET", "/v1/users/aiko-42/sessions?limit="+limit, f.product, ""); rec.Code != nethttp.StatusOK {
			t.Errorf("limit %s: %d, want 200", limit, rec.Code)
		}
	}
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=-1", "?limit=many", "?before=aiko-42", "?before=" + uuid.NewV7().String() + "x"} {
		assertProblem(t, f.send("GET", "/v1/users/aiko-42/sessions"+query, f.product, ""), nethttp.StatusBadRequest)
	}
}

func TestAListWithoutALimitHoldsTwentySessions(t *testing.T) {
	f := newProductFixture(t)
	f.sessionsFor(t, "aiko-42", conversation.PastDefaultLimit+1)
	if got := len(f.list(t, "aiko-42", "").Sessions); got != 20 {
		t.Errorf("a list with no limit holds %d sessions, want 20", got)
	}
}

func TestAnUnknownUserHasAnEmptyList(t *testing.T) {
	f := newProductFixture(t)
	f.sessionsFor(t, "aiko-42", 1)
	rec := f.send("GET", "/v1/users/nobody/sessions", f.product, "")
	if rec.Code != nethttp.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"sessions":[]}` {
		t.Errorf("list for an unknown user: %d %s, want 200 with an empty list", rec.Code, rec.Body)
	}
}

func TestARunningSessionIsListedWithoutAnEndOrASummaryAndAnEndedOneWithBoth(t *testing.T) {
	f := newProductFixture(t)
	ids := f.sessionsFor(t, "aiko-42", 2)
	f.do("POST", "/v1/sessions/"+ids[0]+"/end", f.product)
	ended := uuid.MustParse(ids[0])
	session, _, _ := f.sessions.InWorkspace(context.Background(), workspace, ended)
	if _, err := usercontext.NewService(f.items).SaveSummary(context.Background(), usercontext.SummaryRequest{
		Workspace: workspace, User: session.User, Agent: keeperID, Session: ended, SessionEnded: session.EndedAt, Content: "Covered the lamp.",
	}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}

	rec := f.send("GET", "/v1/users/aiko-42/sessions", f.product, "")
	if body := rec.Body.String(); !strings.Contains(body, `"ended_at":null,"summary":null`) {
		t.Errorf("list = %s, want the running session with a null end and summary", body)
	}
	got := decode[pastList](t, rec).Sessions
	if running, done := got[0], got[1]; running.ID != ids[1] || running.EndedAt != nil || running.Summary != nil ||
		done.ID != ids[0] || done.EndedAt == nil || done.Summary == nil || *done.Summary != "Covered the lamp." {
		t.Errorf("list = %+v, want the running session bare and the ended one with its end and summary", got)
	}
}

func TestASessionIsReadWithItsTurnsAsHeardAndItsSummary(t *testing.T) {
	f := newProductFixture(t)
	_, started := f.start(t, "aiko-42", uuid.NewV7())
	if rec := f.turn(started.ID, started.Ticket); rec.Code != nethttp.StatusOK {
		t.Fatalf("turn: %d %s", rec.Code, rec.Body)
	}
	f.do("POST", "/v1/sessions/"+started.ID+"/end", started.Ticket)
	id := uuid.MustParse(started.ID)
	session, _, _ := f.sessions.InWorkspace(context.Background(), workspace, id)
	if _, err := usercontext.NewService(f.items).SaveSummary(context.Background(), usercontext.SummaryRequest{
		Workspace: workspace, User: session.User, Agent: keeperID, Session: id, SessionEnded: session.EndedAt, Content: "Covered the lamp.",
	}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}

	rec := f.send("GET", "/v1/sessions/"+started.ID, f.product, "")
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	got := decode[pastSession](t, rec)
	if got.ID != started.ID || got.User != "aiko-42" || got.EndedAt == nil || got.Summary == nil || *got.Summary != "Covered the lamp." {
		t.Fatalf("session = %+v, want it ended, with the summary and the user's reference", got)
	}
	if len(got.Turns) != 1 || got.Turns[0].Seq != 1 || got.Turns[0].Words != "Is the lamp lit?" || got.Turns[0].Reply != "Bearing 040." || got.Turns[0].Cut {
		t.Errorf("turns = %+v, want the one turn as heard", got.Turns)
	}
	if strings.Contains(rec.Body.String(), session.User.String()) {
		t.Error("the read names voice-guide's own user ID")
	}
}

func TestACutTurnIsReadAsFarAsTheUserHeardIt(t *testing.T) {
	f := newProductFixture(t)
	_, started := f.start(t, "aiko-42", uuid.NewV7())
	f.turn(started.ID, started.Ticket)
	turn := f.turns.All()[0]
	turn.Heard = 7
	if err := f.turns.Cut(context.Background(), turn); err != nil {
		t.Fatalf("Cut: %v", err)
	}

	got := decode[pastSession](t, f.send("GET", "/v1/sessions/"+started.ID, f.product, ""))
	if len(got.Turns) != 1 || got.Turns[0].Reply != "Bearing" || !got.Turns[0].Cut {
		t.Errorf("turns = %+v, want the reply cut after what was heard", got.Turns)
	}
}

func TestARunningSessionIsReadWithoutAnEndOrASummary(t *testing.T) {
	f := newProductFixture(t)
	_, started := f.start(t, "aiko-42", uuid.NewV7())
	got := decode[pastSession](t, f.send("GET", "/v1/sessions/"+started.ID, f.product, ""))
	if got.EndedAt != nil || got.Summary != nil || got.Turns == nil || len(got.Turns) != 0 {
		t.Errorf("session = %+v, want no end, no summary and an empty list of turns", got)
	}
}

func TestAnotherWorkspacesSessionIsNotFoundAndItsUserHasNoList(t *testing.T) {
	f := newProductFixture(t)
	ids := f.sessionsFor(t, "aiko-42", 1)
	other := f.productKeyFor(t, uuid.MustParse("01a1de8a-0000-7000-8000-000000000002"))
	assertProblem(t, f.send("GET", "/v1/sessions/"+ids[0], other, ""), nethttp.StatusNotFound)
	assertProblem(t, f.send("GET", "/v1/sessions/"+uuid.NewV7().String(), f.product, ""), nethttp.StatusNotFound)
	assertProblem(t, f.send("GET", "/v1/sessions/not-an-id", f.product, ""), nethttp.StatusNotFound)
	if rec := f.send("GET", "/v1/users/aiko-42/sessions", other, ""); strings.TrimSpace(rec.Body.String()) != `{"sessions":[]}` {
		t.Errorf("another workspace's list = %s, want none of this workspace's sessions", rec.Body)
	}
}

func TestATicketIsRefusedOnBothPastSessionRoutes(t *testing.T) {
	f := newProductFixture(t)
	_, started := f.start(t, "aiko-42", uuid.NewV7())
	for _, path := range []string{"/v1/sessions/" + started.ID, "/v1/users/aiko-42/sessions"} {
		assertProblem(t, f.send("GET", path, started.Ticket, ""), nethttp.StatusUnauthorized)
		assertProblem(t, f.send("GET", path, "", ""), nethttp.StatusUnauthorized)
	}
}

func TestAFailedReadAnswersWithoutTheReason(t *testing.T) {
	f := fixtureOverStores(t, newFixtureStore(t), brokenSessions{}, itemsfake.New())
	rec := f.send("GET", "/v1/sessions/"+uuid.NewV7().String(), f.productKey(t), "")
	assertProblem(t, rec, nethttp.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("body = %s, want the failure's reason kept from the caller", rec.Body)
	}
}

// A two-hour session at a turn every half minute is read whole: the size of the answer is
// what a product's server takes in, so it is looked at here, and logged with -v.
func TestATwoHourSessionIsReadWholeAndItsSizeIsSeen(t *testing.T) {
	f := newProductFixture(t)
	_, started := f.start(t, "aiko-42", uuid.NewV7())
	id := uuid.MustParse(started.ID)
	session, _, _ := f.sessions.InWorkspace(context.Background(), workspace, id)
	const turns = 240 // two hours at one turn every half minute
	words, reply := strings.Repeat("w", 150), strings.Repeat("r", 750)
	for i := 0; i < turns; i++ {
		turn := conversation.Turn{ID: uuid.NewV7(), Workspace: workspace, User: session.User, Session: id, RequestID: uuid.NewV7(), Words: words, Status: conversation.TurnInFlight}
		if _, err := f.turns.Start(context.Background(), turn, i, time.Minute); err != nil {
			t.Fatalf("Start turn %d: %v", i+1, err)
		}
		turn.Status, turn.Reply = conversation.TurnAnswered, reply
		if err := f.turns.Finish(context.Background(), turn); err != nil {
			t.Fatalf("Finish turn %d: %v", i+1, err)
		}
	}

	rec := f.send("GET", "/v1/sessions/"+started.ID, f.product, "")
	size := rec.Body.Len()
	if got := decode[pastSession](t, rec); rec.Code != nethttp.StatusOK || len(got.Turns) != turns || got.Turns[turns-1].Seq != turns {
		t.Fatalf("read: %d with %d turns, want all %d", rec.Code, len(got.Turns), turns)
	}
	t.Logf("a session of %d turns of about 900 characters is read as %d KiB (%d bytes)", turns, size>>10, size)
}
