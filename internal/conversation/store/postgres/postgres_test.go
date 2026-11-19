package postgres_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/conversation/store/postgres"
	"github.com/mikaclements/voice-guide/internal/pgtest"
)

var (
	workspace = uuid.MustParse("01a1de8a-0000-7000-8000-000000000001")
	elsewhere = uuid.MustParse("01a1de8a-0000-7000-8000-000000000002")
	user      = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000c1")
	keeperID  = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a1")
	captainID = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a2")
)

// storeWithTwoWorkspaces returns a store on a fresh database holding two
// workspaces, each with one agent at version 1, and the pool beneath it.
func storeWithTwoWorkspaces(t *testing.T) (*postgres.Sessions, *pgxpool.Pool) {
	t.Helper()
	pool := pgtest.New(t)
	pgtest.AddWorkspaces(t, pool, workspace, elsewhere)
	for ws, agent := range map[uuid.UUID]uuid.UUID{workspace: keeperID, elsewhere: captainID} {
		pgtest.Exec(t, pool, "INSERT INTO agent.agents (id, workspace_id) VALUES ($1, $2)", agent, ws)
		pgtest.Exec(t, pool, `INSERT INTO agent.agent_versions (workspace_id, agent_id, version, name, description, reads, persona, content_hash)
			VALUES ($1, $2, 1, 'Harbour hand', '', '{}', 'You answer every call from the sea.', 'hash')`, ws, agent)
	}
	return postgres.NewSessions(pool), pool
}

// sessionOn builds a new session for the user on version 1 of the workspace's agent.
func sessionOn(ws, u uuid.UUID) conversation.Session {
	agent := keeperID
	if ws == elsewhere {
		agent = captainID
	}
	return conversation.Session{ID: uuid.NewV7(), Workspace: ws, User: u, Agent: agent, AgentVersion: 1}
}

func start(t *testing.T, s *postgres.Sessions, sess conversation.Session, request uuid.UUID) (conversation.Session, bool) {
	t.Helper()
	got, isNew, err := s.Start(context.Background(), sess, request, "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return got, isNew
}

func TestAStartedSessionIsSavedWithItsAgentVersionAndANewUser(t *testing.T) {
	s, _ := storeWithTwoWorkspaces(t)
	want := sessionOn(workspace, user)
	got, isNew := start(t, s, want, uuid.NewV7())
	if !isNew || got.ID != want.ID || got.Workspace != workspace || got.User != user || got.AgentVersion != 1 || got.StartedAt.IsZero() {
		t.Errorf("Start = %+v, %v; want the new session, with its start time", got, isNew)
	}
}

func TestARequestIDIsAnsweredOnlyWithinItsWorkspace(t *testing.T) {
	s, _ := storeWithTwoWorkspaces(t)
	request := uuid.NewV7()
	here, _ := start(t, s, sessionOn(workspace, user), request)
	there, _ := start(t, s, sessionOn(elsewhere, user), request)

	again, isNew := start(t, s, sessionOn(elsewhere, user), request)
	if isNew || again.ID != there.ID || again.Workspace != elsewhere || there.ID == here.ID {
		t.Errorf("repeat in the other workspace = %+v, new %v; want that workspace's own session %v", again, isNew, there.ID)
	}
}

func TestOneRequestIDFromTwoUsersMakesTwoSessions(t *testing.T) {
	s, _ := storeWithTwoWorkspaces(t)
	request := uuid.NewV7()
	mine, _ := start(t, s, sessionOn(workspace, user), request)
	theirs, isNew := start(t, s, sessionOn(workspace, uuid.NewV7()), request)
	if !isNew || theirs.ID == mine.ID {
		t.Errorf("another user's start = %+v, new %v; want their own session", theirs, isNew)
	}
}

func TestAStartWaitingOnAnUncommittedStartGetsThatSession(t *testing.T) {
	s, pool := storeWithTwoWorkspaces(t)
	ctx := context.Background()
	request, first := uuid.NewV7(), sessionOn(workspace, user)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "INSERT INTO users (id) VALUES ($1)", user); err != nil {
		t.Fatalf("add user: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO sessions (id, workspace_id, user_id, agent_id, agent_version, request_id) VALUES ($1, $2, $3, $4, 1, $5)",
		first.ID, workspace, user, keeperID, request); err != nil {
		t.Fatalf("add session: %v", err)
	}

	type result struct {
		s     conversation.Session
		isNew bool
		err   error
	}
	done := make(chan result, 1)
	go func() {
		got, isNew, err := s.Start(ctx, sessionOn(workspace, user), request, "")
		done <- result{got, isNew, err}
	}()
	waitForABlockedQuery(t, pool)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	r := <-done
	if r.err != nil || r.isNew || r.s.ID != first.ID {
		t.Errorf("waiting start = %+v, new %v, %v; want the first start's session %v", r.s, r.isNew, r.err, first.ID)
	}
}

// waitForABlockedQuery returns once a query in this test's database waits on
// a lock, so the test controls the order of events instead of sleeping.
func waitForABlockedQuery(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var blocked int
		if err := pool.QueryRow(context.Background(),
			"SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'").Scan(&blocked); err != nil {
			t.Fatalf("read activity: %v", err)
		}
		if blocked > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no query blocked on the uncommitted start within 5s")
}

func TestASessionOnAnotherWorkspacesAgentIsRefused(t *testing.T) {
	s, _ := storeWithTwoWorkspaces(t)
	stray := conversation.Session{ID: uuid.NewV7(), Workspace: elsewhere, User: user, Agent: keeperID, AgentVersion: 1}
	if _, _, err := s.Start(context.Background(), stray, uuid.NewV7(), ""); err == nil {
		t.Error("Start saved a session on another workspace's agent")
	}
}

func TestASessionNeedsAPublishedVersion(t *testing.T) {
	s, _ := storeWithTwoWorkspaces(t)
	unpublished := sessionOn(workspace, user)
	unpublished.AgentVersion = 2
	if _, _, err := s.Start(context.Background(), unpublished, uuid.NewV7(), ""); err == nil {
		t.Error("Start saved a session on a version nobody published")
	}
}

func TestTheContextItemsASessionLoadedAreSavedAndARepeatReturnsThem(t *testing.T) {
	s, _ := storeWithTwoWorkspaces(t)
	request := uuid.NewV7()
	sess := sessionOn(workspace, user)
	sess.ContextItems = []uuid.UUID{uuid.NewV7(), uuid.NewV7()}
	first, _ := start(t, s, sess, request)

	again, _ := start(t, s, sessionOn(workspace, user), request)
	if !slices.Equal(first.ContextItems, sess.ContextItems) || !slices.Equal(again.ContextItems, sess.ContextItems) {
		t.Errorf("items = %v, then %v on the repeat; want %v both times, in order", first.ContextItems, again.ContextItems, sess.ContextItems)
	}
}

func TestForRequestFindsOnlyTheUsersOwnSessionInTheWorkspace(t *testing.T) {
	s, _ := storeWithTwoWorkspaces(t)
	request := uuid.NewV7()
	mine, _ := start(t, s, sessionOn(workspace, user), request)

	tests := map[string]struct {
		ws, user uuid.UUID
		want     bool
	}{
		"the same user and workspace": {workspace, user, true},
		"another workspace":           {elsewhere, user, false},
		"another user":                {workspace, uuid.NewV7(), false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, found, err := s.ForRequest(context.Background(), tt.ws, tt.user, request)
			if err != nil || found != tt.want || (found && got.ID != mine.ID) {
				t.Errorf("ForRequest = %+v, %v, %v; want found %v", got, found, err, tt.want)
			}
		})
	}
}

func TestByIDFindsOnlyTheUsersOwnSessionInTheWorkspace(t *testing.T) {
	s, _ := storeWithTwoWorkspaces(t)
	mine, _ := start(t, s, sessionOn(workspace, user), uuid.NewV7())
	if got, found, err := s.ByID(context.Background(), workspace, user, mine.ID); !found || err != nil || got.ID != mine.ID {
		t.Errorf("ByID for the session's own user = %+v, %v, %v; want the session", got, found, err)
	}
	for _, c := range []access.Caller{{Workspace: elsewhere, User: user}, {Workspace: workspace, User: uuid.NewV7()}} {
		if got, found, err := s.ByID(context.Background(), c.Workspace, c.User, mine.ID); found || err != nil {
			t.Errorf("ByID for %+v = %+v, %v, %v; want nothing", c, got, found, err)
		}
	}
}

func TestInWorkspaceFindsAnyUsersSessionButOnlyInItsWorkspace(t *testing.T) {
	s, _ := storeWithTwoWorkspaces(t)
	mine, _ := start(t, s, sessionOn(workspace, user), uuid.NewV7())
	if got, found, err := s.InWorkspace(context.Background(), workspace, mine.ID); !found || err != nil || got.ID != mine.ID || got.User != user {
		t.Errorf("InWorkspace in the session's workspace = %+v, %v, %v; want the session and its user", got, found, err)
	}
	if got, found, err := s.InWorkspace(context.Background(), elsewhere, mine.ID); found || err != nil {
		t.Errorf("InWorkspace in another workspace = %+v, %v, %v; want nothing", got, found, err)
	}
}

func TestTheSessionContextIsSavedInOrderAndARepeatReturnsIt(t *testing.T) {
	s, _ := storeWithTwoWorkspaces(t)
	request := uuid.NewV7()
	sess := sessionOn(workspace, user)
	sess.SessionContext = []conversation.SessionContextBlock{{Name: "resume", Content: "Deckhand, \"two\" seasons.\nBosun after."}, {Name: "page", Content: "海図"}}
	first, _ := start(t, s, sess, request)

	again, _ := start(t, s, sessionOn(workspace, user), request)
	if !slices.Equal(first.SessionContext, sess.SessionContext) || !slices.Equal(again.SessionContext, sess.SessionContext) {
		t.Errorf("session context = %+v, then %+v on the repeat; want %+v both times", first.SessionContext, again.SessionContext, sess.SessionContext)
	}
}

func TestASessionStartedWithoutSessionContextHoldsAnEmptyList(t *testing.T) {
	s, pool := storeWithTwoWorkspaces(t)
	got, _ := start(t, s, sessionOn(workspace, user), uuid.NewV7())
	var stored string
	if err := pool.QueryRow(context.Background(), "SELECT session_context::text FROM sessions WHERE id = $1", got.ID).Scan(&stored); err != nil {
		t.Fatalf("read the column: %v", err)
	}
	if len(got.SessionContext) != 0 || stored != "[]" {
		t.Errorf("session context = %+v, column %q; want none, stored as an empty list", got.SessionContext, stored)
	}
}

// count returns how many rows the table holds.
func count(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestAStartForANewReferenceAddsItsUserWithTheSession(t *testing.T) {
	s, _ := storeWithTwoWorkspaces(t)
	ctx := context.Background()
	got, isNew, err := s.Start(ctx, sessionOn(workspace, user), uuid.NewV7(), "aiko-42")
	if err != nil || !isNew || got.User != user {
		t.Fatalf("Start = %+v, new %v, %v; want a new session for the new user", got, isNew, err)
	}
	if found, ok, err := s.UserByReference(ctx, workspace, "aiko-42"); err != nil || !ok || found != user {
		t.Errorf("UserByReference = %v, %v, %v; want %v", found, ok, err, user)
	}
	if _, ok, err := s.UserByReference(ctx, elsewhere, "aiko-42"); err != nil || ok {
		t.Errorf("UserByReference in another workspace = %v, %v; want none", ok, err)
	}
}

func TestAStartThatFailsAddsNeitherUserNorReference(t *testing.T) {
	s, pool := storeWithTwoWorkspaces(t)
	unpublished := sessionOn(workspace, user)
	unpublished.AgentVersion = 2
	if _, _, err := s.Start(context.Background(), unpublished, uuid.NewV7(), "aiko-42"); err == nil {
		t.Fatal("Start saved a session on a version nobody published")
	}
	if users, references := count(t, pool, "users"), count(t, pool, "user_references"); users != 0 || references != 0 {
		t.Errorf("%d users, %d references; want none", users, references)
	}
}

func TestAStartWaitingOnAnotherForTheSameNewReferenceGetsThatUserAndAddsNoOther(t *testing.T) {
	s, pool := storeWithTwoWorkspaces(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "INSERT INTO user_references (workspace_id, reference, user_id) VALUES ($1, 'aiko-42', $2)", workspace, user); err != nil {
		t.Fatalf("add reference: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO users (id) VALUES ($1)", user); err != nil {
		t.Fatalf("add user: %v", err)
	}
	type result struct {
		s   conversation.Session
		err error
	}
	done := make(chan result, 1)
	go func() {
		got, _, err := s.Start(ctx, sessionOn(workspace, uuid.NewV7()), uuid.NewV7(), "aiko-42")
		done <- result{got, err}
	}()
	waitForABlockedQuery(t, pool)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if r := <-done; r.err != nil || r.s.User != user {
		t.Errorf("waiting start = %+v, %v; want a session for the first start's user %v", r.s, r.err, user)
	}
	if users := count(t, pool, "users"); users != 1 {
		t.Errorf("%d users; want only the first start's", users)
	}
}

// A new user's first start pressed twice: the second, with the same reference and request ID,
// waits on the first's uncommitted writes, then gets its session and adds nothing.
func TestANewUsersFirstStartPressedTwiceMakesOneSessionOneUserOneReference(t *testing.T) {
	s, pool := storeWithTwoWorkspaces(t)
	ctx := context.Background()
	request, first := uuid.NewV7(), sessionOn(workspace, user)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, w := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO user_references (workspace_id, reference, user_id) VALUES ($1, 'aiko-42', $2)", []any{workspace, user}},
		{"INSERT INTO users (id) VALUES ($1)", []any{user}},
		{"INSERT INTO sessions (id, workspace_id, user_id, agent_id, agent_version, request_id) VALUES ($1, $2, $3, $4, 1, $5)", []any{first.ID, workspace, user, keeperID, request}},
	} {
		if _, err := tx.Exec(ctx, w.sql, w.args...); err != nil {
			t.Fatalf("%s: %v", w.sql, err)
		}
	}
	type result struct {
		s     conversation.Session
		isNew bool
		err   error
	}
	done := make(chan result, 1)
	go func() {
		got, isNew, err := s.Start(ctx, sessionOn(workspace, uuid.NewV7()), request, "aiko-42")
		done <- result{got, isNew, err}
	}()
	waitForABlockedQuery(t, pool)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if r := <-done; r.err != nil || r.isNew || r.s.ID != first.ID || r.s.User != user {
		t.Errorf("second press = %+v, new %v, %v; want the first press's session %v, not new", r.s, r.isNew, r.err, first.ID)
	}
	for table, want := range map[string]int{"sessions": 1, "users": 1, "user_references": 1} {
		if got := count(t, pool, table); got != want {
			t.Errorf("%d %s; want %d", got, table, want)
		}
	}
}

func TestAUserHasOneReferenceInAWorkspace(t *testing.T) {
	s, pool := storeWithTwoWorkspaces(t)
	start(t, s, sessionOn(workspace, user), uuid.NewV7())
	pgtest.Exec(t, pool, "INSERT INTO user_references (workspace_id, reference, user_id) VALUES ($1, 'aiko-42', $2)", workspace, user)
	if _, err := pool.Exec(context.Background(), "INSERT INTO user_references (workspace_id, reference, user_id) VALUES ($1, 'aiko', $2)", workspace, user); err == nil {
		t.Error("a second reference for the same user was saved")
	}
}

func TestAUsersSessionsListNewestFirstFromTheCursorAndOnlyTheirOwnInTheirWorkspace(t *testing.T) {
	s, _ := storeWithTwoWorkspaces(t)
	ctx := context.Background()
	var mine []uuid.UUID
	for range 5 {
		got, _ := start(t, s, sessionOn(workspace, user), uuid.NewV7())
		mine = append(mine, got.ID)
	}
	start(t, s, sessionOn(workspace, uuid.NewV7()), uuid.NewV7()) // another user's
	start(t, s, sessionOn(elsewhere, user), uuid.NewV7())         // the same user ID in another workspace
	newest := []uuid.UUID{mine[4], mine[3], mine[2], mine[1], mine[0]}

	ids := func(before uuid.UUID, limit int, ws uuid.UUID) []uuid.UUID {
		t.Helper()
		list, err := s.ListForUser(ctx, ws, user, before, limit)
		if err != nil {
			t.Fatalf("ListForUser: %v", err)
		}
		got := []uuid.UUID{}
		for _, session := range list {
			got = append(got, session.ID)
		}
		return got
	}
	if got := ids(uuid.Nil(), 10, workspace); !slices.Equal(got, newest) {
		t.Errorf("list = %v, want the user's five newest first: %v", got, newest)
	}
	if got := ids(uuid.Nil(), 2, workspace); !slices.Equal(got, newest[:2]) {
		t.Errorf("first page = %v, want %v", got, newest[:2])
	}
	if got := ids(newest[1], 2, workspace); !slices.Equal(got, newest[2:4]) {
		t.Errorf("page after the second = %v, want %v", got, newest[2:4])
	}
	if got := ids(newest[4], 2, workspace); len(got) != 0 {
		t.Errorf("page after the oldest = %v, want none", got)
	}
	if got := ids(uuid.Nil(), 10, elsewhere); len(got) != 1 || slices.Contains(newest, got[0]) {
		t.Errorf("the other workspace's list = %v, want only its own one session", got)
	}
}

// A product's server may read a session only in its own workspace: another's is
// ErrNoSuchSession, as one that never existed, whatever it is asked through.
func TestAnotherWorkspacesProductCannotReadASessionOrListItsUser(t *testing.T) {
	s, pool := storeWithTwoWorkspaces(t)
	ctx := context.Background()
	got, _, err := s.Start(ctx, sessionOn(workspace, user), uuid.NewV7(), "aiko-42")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The session is live, so no summary is read and the service needs no user context.
	svc := conversation.NewService(nil, s, postgres.NewTurns(pool), nil, nil, conversation.Settings{})

	if past, _, err := svc.PastSession(ctx, access.Caller{Workspace: workspace, Product: true}, got.ID); err != nil || past.Reference != "aiko-42" {
		t.Errorf("PastSession in its own workspace = %+v, %v; want the session with the product's reference", past, err)
	}
	if _, _, err := svc.PastSession(ctx, access.Caller{Workspace: elsewhere, Product: true}, got.ID); !errors.Is(err, conversation.ErrNoSuchSession) {
		t.Errorf("PastSession in another workspace = %v, want ErrNoSuchSession", err)
	}
	if list, err := svc.PastSessions(ctx, access.Caller{Workspace: elsewhere, Product: true}, "aiko-42", uuid.Nil(), conversation.PastDefaultLimit); err != nil || len(list) != 0 {
		t.Errorf("PastSessions for the same reference in another workspace = %v, %v; want none", list, err)
	}
	if list, err := svc.PastSessions(ctx, access.Caller{Workspace: workspace, Product: true}, "aiko-42", uuid.Nil(), conversation.PastDefaultLimit); err != nil || len(list) != 1 {
		t.Errorf("PastSessions in its own workspace = %v, %v; want the one session", list, err)
	}
	if _, _, err := svc.PastSession(ctx, access.Caller{Workspace: workspace, User: user, Session: got.ID}, got.ID); !errors.Is(err, conversation.ErrNoSuchSession) {
		t.Errorf("PastSession for a caller that isn't a product = %v, want ErrNoSuchSession", err)
	}
}
