package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/conversation/store/postgres"
	"github.com/mikaclements/voice-guide/internal/pgtest"
)

// idle is how long a session in these tests goes with no turn before it ends.
const idle = 5 * time.Minute

func TestEndingASessionKeepsTheTimeItFirstEnded(t *testing.T) {
	sessions, _ := storeWithTwoWorkspaces(t)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	ctx := context.Background()

	ended, now, err := sessions.End(ctx, workspace, user, s.ID)
	if err != nil || !now || !ended.Ended() || ended.EndedAt.Before(s.StartedAt) {
		t.Fatalf("End = %+v, %v, %v; want the session ended by this call", ended, now, err)
	}
	again, now, err := sessions.End(ctx, workspace, user, s.ID)
	if err != nil || now || !again.EndedAt.Equal(ended.EndedAt) {
		t.Errorf("End again = %+v, %v, %v; want it ended at %s, as first, not by this call", again, now, err, ended.EndedAt)
	}
	if read, _, err := sessions.ByID(ctx, workspace, user, s.ID); err != nil || !read.EndedAt.Equal(ended.EndedAt) {
		t.Errorf("ByID = %+v, %v; want it read back ended", read, err)
	}
}

func TestOnlyTheUsersOwnSessionInItsWorkspaceCanBeEnded(t *testing.T) {
	sessions, _ := storeWithTwoWorkspaces(t)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	ctx := context.Background()
	for name, by := range map[string][2]uuid.UUID{
		"another workspace": {elsewhere, user},
		"another user":      {workspace, uuid.NewV7()},
	} {
		if got, now, err := sessions.End(ctx, by[0], by[1], s.ID); !errors.Is(err, conversation.ErrNoSuchSession) || now {
			t.Errorf("%s: End = %+v, %v, %v; want ErrNoSuchSession", name, got, now, err)
		}
	}
	if read, _, _ := sessions.ByID(ctx, workspace, user, s.ID); read.Ended() {
		t.Errorf("the session ended at %s; want it live", read.EndedAt)
	}
}

func TestATurnIsRefusedOnceItsSessionHasEndedAndOneInFlightStillFinishes(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	ctx := context.Background()
	inFlight := turnOf(s, "Is the lamp lit?")
	startTurn(t, turns, inFlight)
	if _, _, err := sessions.End(ctx, workspace, user, s.ID); err != nil {
		t.Fatalf("End: %v", err)
	}

	finish(t, turns, &inFlight, conversation.TurnAnswered, "Bearing 040.")
	if _, err := turns.Start(ctx, turnOf(s, "And the fog?"), 1, deadline); !errors.Is(err, conversation.ErrSessionEnded) {
		t.Errorf("Start after the end = %v, want ErrSessionEnded", err)
	}
}

func TestATurnWaitingOnAnUncommittedEndIsRefusedOnceItCommits(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "UPDATE sessions SET ended_at = now() WHERE id = $1", s.ID); err != nil {
		t.Fatalf("end: %v", err)
	}

	refused := make(chan error, 1)
	go func() {
		_, err := turns.Start(ctx, turnOf(s, "Is the lamp lit?"), 0, deadline)
		refused <- err
	}()
	waitForABlockedQuery(t, pool)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := <-refused; !errors.Is(err, conversation.ErrSessionEnded) {
		t.Errorf("the waiting Start = %v, want ErrSessionEnded", err)
	}
}

// idleFor moves when the session was last active back by ago: the clock is the database's, so the
// time is moved rather than waited for.
func idleFor(t *testing.T, pool *pgxpool.Pool, session uuid.UUID, ago time.Duration) {
	t.Helper()
	pgtest.Exec(t, pool, "UPDATE sessions SET last_active_at = now() - make_interval(secs => $2) WHERE id = $1", session, ago.Seconds())
}

func TestASessionEndsOnceItHasNotBeenActiveForTheIdleTime(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	ctx := context.Background()
	tests := map[string]struct {
		ws    uuid.UUID
		ago   time.Duration
		then  func(s conversation.Session) // what happens after it went quiet, if anything
		ended bool
	}{
		"just short of idle":        {workspace, idle - time.Second, nil, false},
		"just past idle":            {workspace, idle + time.Second, nil, true},
		"idle in another workspace": {elsewhere, idle + time.Second, nil, true},
		"a turn started since": {workspace, time.Hour, func(s conversation.Session) {
			startTurn(t, turns, turnOf(s, "Is the lamp lit?"))
		}, false},
		"the user heard since": {workspace, time.Hour, func(s conversation.Session) {
			if live, err := sessions.Touch(ctx, s.Workspace, s.User, s.ID); !live || err != nil {
				t.Fatalf("Touch = %v, %v", live, err)
			}
		}, false},
	}
	ids := map[string]uuid.UUID{}
	for name, tt := range tests {
		s, _ := start(t, sessions, sessionOn(tt.ws, user), uuid.NewV7())
		ids[name] = s.ID
		idleFor(t, pool, s.ID, tt.ago)
		if tt.then != nil {
			tt.then(s)
		}
	}
	ended, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	if _, _, err := sessions.End(ctx, workspace, user, ended.ID); err != nil {
		t.Fatalf("End: %v", err)
	}
	pgtest.Exec(t, pool, "UPDATE sessions SET last_active_at = now() - interval '1 hour', ended_at = now() - interval '50 minutes' WHERE id = $1", ended.ID)

	endedIDs, err := sessions.EndIdle(ctx, idle)
	if err != nil || len(endedIDs) != 2 {
		t.Errorf("EndIdle = %v, %v; want the 2 idle sessions ended", endedIDs, err)
	}
	for name, tt := range tests {
		s, _, _ := sessions.ByID(ctx, tt.ws, user, ids[name])
		if s.Ended() != tt.ended {
			t.Errorf("%s: ended %v, want %v", name, s.Ended(), tt.ended)
		}
	}
	if s, _, _ := sessions.ByID(ctx, workspace, user, ended.ID); s.EndedAt.After(time.Now().Add(-49 * time.Minute)) {
		t.Errorf("a session already ended was ended again, at %s", s.EndedAt)
	}
}

func TestASweepWaitingOnATurnStartingInAnIdleSessionLeavesItLive(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	idleFor(t, pool, s.ID, time.Hour)
	ctx := context.Background()
	// A turn starting marks the session active and holds its row until the turn is saved; this
	// is that write, held open.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "UPDATE sessions SET last_active_at = now() WHERE id = $1 AND ended_at IS NULL", s.ID); err != nil {
		t.Fatalf("mark active: %v", err)
	}

	swept := make(chan int, 1)
	go func() {
		ids, err := sessions.EndIdle(ctx, idle)
		if err != nil {
			t.Errorf("EndIdle: %v", err)
		}
		swept <- len(ids)
	}()
	waitForABlockedQuery(t, pool)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if n := <-swept; n != 0 {
		t.Errorf("the waiting sweep ended %d sessions, want none: a turn had just started", n)
	}
	if read, _, _ := sessions.ByID(ctx, workspace, user, s.ID); read.Ended() {
		t.Errorf("the session ended at %s under a turn that had just started", read.EndedAt)
	}
}

func TestOnlyTheUsersOwnLiveSessionIsMarkedActive(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	ctx := context.Background()
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	for name, by := range map[string][2]uuid.UUID{"another workspace": {elsewhere, user}, "another user": {workspace, uuid.NewV7()}} {
		if live, err := sessions.Touch(ctx, by[0], by[1], s.ID); live || err != nil {
			t.Errorf("%s: Touch = %v, %v; want nothing marked", name, live, err)
		}
	}
	if _, _, err := sessions.End(ctx, workspace, user, s.ID); err != nil {
		t.Fatalf("End: %v", err)
	}
	idleFor(t, pool, s.ID, time.Hour)
	if live, err := sessions.Touch(ctx, workspace, user, s.ID); live || err != nil {
		t.Errorf("Touch of an ended session = %v, %v; want nothing marked", live, err)
	}
}

func TestTwoSweepsAtOnceEndEachIdleSessionOnce(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	const idleSessions = 50
	for range idleSessions {
		s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
		idleFor(t, pool, s.ID, time.Hour)
	}
	other := postgres.New(pool) // another instance, sweeping at the same time
	var wg sync.WaitGroup
	counts := make([]int, 2)
	for i, sweeper := range []*postgres.Sessions{sessions, other} {
		wg.Go(func() {
			ids, err := sweeper.EndIdle(context.Background(), idle)
			if err != nil {
				t.Errorf("EndIdle: %v", err)
			}
			counts[i] = len(ids)
		})
	}
	wg.Wait()
	if counts[0]+counts[1] != idleSessions {
		t.Errorf("the sweeps ended %d and %d sessions, want %d in all, each once", counts[0], counts[1], idleSessions)
	}
	if n := summaryJobs(t, pool); n != idleSessions {
		t.Errorf("%d summary jobs queued, want one per session ended, %d", n, idleSessions)
	}
}

// summaryJobs counts the summary jobs queued, checking each names a session ended, in its own
// workspace, for its own user.
func summaryJobs(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n, wrong int
	err := pool.QueryRow(context.Background(), `
		SELECT count(*), count(*) FILTER (WHERE s.id IS NULL OR s.ended_at IS NULL)
		FROM jobs j LEFT JOIN sessions s
		  ON s.id = (j.args->>'session_id')::uuid AND s.workspace_id = j.workspace_id AND s.user_id = j.user_id
		WHERE j.kind = $1`, conversation.SummaryJob).Scan(&n, &wrong)
	if err != nil {
		t.Fatalf("count the summary jobs: %v", err)
	}
	if wrong != 0 {
		t.Errorf("%d summary jobs name no ended session of their workspace and user", wrong)
	}
	return n
}

func TestEndingASessionQueuesItsSummaryOnceHoweverManyEndsRace(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	var ends sync.WaitGroup
	var mu sync.Mutex
	endedNow := 0
	for range 8 {
		ends.Go(func() {
			_, now, err := sessions.End(context.Background(), workspace, user, s.ID)
			if err != nil {
				t.Errorf("End: %v", err)
			}
			if now {
				mu.Lock()
				endedNow++
				mu.Unlock()
			}
		})
	}
	ends.Wait()
	if endedNow != 1 || summaryJobs(t, pool) != 1 {
		t.Errorf("%d ends ended it and %d summary jobs were queued, want one of each", endedNow, summaryJobs(t, pool))
	}
	// The args are read as the job runner reads them: every field the summary's, none other.
	var raw []byte
	if err := pool.QueryRow(context.Background(), "SELECT args FROM jobs").Scan(&raw); err != nil {
		t.Fatalf("read the job's args: %v", err)
	}
	var args conversation.SummaryArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil || args.Session != s.ID {
		t.Errorf("the job's args %s = %+v, %v; want the session, read strictly as a summary job's", raw, args, err)
	}
}

func TestATurnInFlightIsFoundUntilItEndsOrOutlivesTheDeadline(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	ctx := context.Background()
	inFlight := func() bool {
		t.Helper()
		got, err := turns.InFlight(ctx, workspace, s.ID, deadline)
		if err != nil {
			t.Fatalf("InFlight: %v", err)
		}
		return got
	}
	turn := turnOf(s, "Is the lamp lit?")
	startTurn(t, turns, turn)
	if !inFlight() {
		t.Error("a turn just started isn't in flight")
	}
	if got, _ := turns.InFlight(ctx, elsewhere, s.ID, deadline); got {
		t.Error("another workspace sees the turn in flight")
	}
	pgtest.Exec(t, pool, "UPDATE turns SET created_at = now() - interval '2 minutes' WHERE id = $1", turn.ID)
	if inFlight() {
		t.Error("a turn past the deadline counts as in flight")
	}
	pgtest.Exec(t, pool, "UPDATE turns SET created_at = now() WHERE id = $1", turn.ID)
	finish(t, turns, &turn, conversation.TurnAnswered, "Bearing 040.")
	if inFlight() {
		t.Error("a finished turn counts as in flight")
	}
}

func TestATurnRefusedAsTheSessionEndedIsAnsweredAsARepeatWhenItIsOne(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	ctx := context.Background()
	taken := turnOf(s, "Is the lamp lit?")
	startTurn(t, turns, taken)
	if _, _, err := sessions.End(ctx, workspace, user, s.ID); err != nil {
		t.Fatalf("End: %v", err)
	}
	retry := taken
	retry.ID = uuid.NewV7()
	if _, err := turns.Start(ctx, retry, 0, deadline); !errors.Is(err, conversation.ErrTurnInFlight) {
		t.Errorf("a retry while the turn is in flight = %v, want ErrTurnInFlight", err)
	}
	finish(t, turns, &taken, conversation.TurnAnswered, "Bearing 040.")
	if _, err := turns.Start(ctx, retry, 1, deadline); !errors.Is(err, conversation.ErrTurnRepeated) {
		t.Errorf("a retry once the turn finished = %v, want ErrTurnRepeated", err)
	}
	if _, err := turns.Start(ctx, turnOf(s, "And the fog?"), 1, deadline); !errors.Is(err, conversation.ErrSessionEnded) {
		t.Errorf("a new turn = %v, want ErrSessionEnded", err)
	}
}

func TestATurnLeftInFlightPastTheDeadlineIsCutOffAsLost(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	ctx := context.Background()
	stale, fresh := turnOf(s, "Is the lamp lit?"), turnOf(s, "And the fog?")
	startTurn(t, turns, stale)
	pgtest.Exec(t, pool, "UPDATE turns SET created_at = now() - interval '2 minutes' WHERE id = $1", stale.ID)
	if err := turns.CutOff(ctx, elsewhere, s.ID, deadline); err != nil {
		t.Fatalf("CutOff from another workspace: %v", err)
	}
	if got, _ := turns.Finished(ctx, workspace, s.ID); len(got) != 0 {
		t.Fatalf("another workspace cut off %+v", got)
	}
	if err := turns.CutOff(ctx, workspace, s.ID, deadline); err != nil {
		t.Fatalf("CutOff: %v", err)
	}
	got, _ := turns.Finished(ctx, workspace, s.ID)
	if len(got) != 1 || got[0].RequestID != stale.RequestID || got[0].Status != conversation.TurnLost {
		t.Errorf("Finished = %+v, want the turn cut off, saved lost", got)
	}
	startTurn(t, turns, fresh)
	if err := turns.CutOff(ctx, workspace, s.ID, deadline); err != nil {
		t.Fatalf("CutOff: %v", err)
	}
	if inFlight, _ := turns.InFlight(ctx, workspace, s.ID, deadline); !inFlight {
		t.Error("a turn within the deadline was cut off")
	}
}
