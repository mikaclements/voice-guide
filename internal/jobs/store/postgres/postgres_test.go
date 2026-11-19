package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/jobs"
	"github.com/mikaclements/voice-guide/internal/jobs/store/postgres"
	"github.com/mikaclements/voice-guide/internal/pgtest"
)

var (
	workspace = uuid.MustParse("01a1de8a-0000-7000-8000-000000000001")
	elsewhere = uuid.MustParse("01a1de8a-0000-7000-8000-000000000002")
	user      = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000c1")
)

const lease = 2 * time.Minute

// lamps are the kinds the claims in these tests take: the one add queues.
var lamps = []string{"lamp"}

// queueOf returns the store on a fresh database with two workspaces, and the pool beneath it.
func queueOf(t *testing.T) (*postgres.Store, *pgxpool.Pool) {
	t.Helper()
	pool := pgtest.New(t)
	pgtest.AddWorkspaces(t, pool, workspace, elsewhere)
	pgtest.Exec(t, pool, "INSERT INTO users (id) VALUES ($1)", user)
	return postgres.New(pool), pool
}

// add queues a job as a module does, inserting its row; ago moves when it became due.
func add(t *testing.T, pool *pgxpool.Pool, ws uuid.UUID, ago time.Duration) uuid.UUID {
	t.Helper()
	id := uuid.NewV7()
	pgtest.Exec(t, pool, `INSERT INTO jobs (id, workspace_id, user_id, kind, args, run_at)
		VALUES ($1, $2, $3, 'lamp', '{"tower":"North"}', now() - make_interval(secs => $4))`, id, ws, user, ago.Seconds())
	return id
}

func state(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) (string, int, string) {
	t.Helper()
	var s, why string
	var attempts int
	if err := pool.QueryRow(context.Background(), "SELECT state, attempts, coalesce(last_error, '') FROM jobs WHERE id = $1", id).Scan(&s, &attempts, &why); err != nil {
		t.Fatalf("read the job: %v", err)
	}
	return s, attempts, why
}

func TestAClaimTakesTheJobDueSoonestInAnyWorkspaceWithItsArgs(t *testing.T) {
	q, pool := queueOf(t)
	later := add(t, pool, workspace, time.Second)
	sooner := add(t, pool, elsewhere, time.Minute)
	add(t, pool, workspace, -time.Hour) // not due for an hour

	j, found, err := q.Claim(context.Background(), lease, lamps)
	if err != nil || !found || j.ID != sooner || j.Workspace != elsewhere || j.User != user || j.Kind != "lamp" || string(j.Args) != `{"tower": "North"}` || j.Attempt != 1 {
		t.Fatalf("Claim = %+v, %v, %v; want the job due soonest, from the other workspace, on its first run", j, found, err)
	}
	if next, _, _ := q.Claim(context.Background(), lease, lamps); next.ID != later {
		t.Errorf("second Claim = %+v, want the next due, %s", next, later)
	}
	if _, found, err := q.Claim(context.Background(), lease, lamps); found || err != nil {
		t.Errorf("third Claim = %v, %v; want none due", found, err)
	}
}

func TestAClaimTakesOnlyTheKindsItAsksFor(t *testing.T) {
	q, pool := queueOf(t)
	add(t, pool, workspace, time.Minute)
	beacon := uuid.NewV7()
	pgtest.Exec(t, pool, `INSERT INTO jobs (id, workspace_id, kind, args) VALUES ($1, $2, 'beacon', '{}')`, beacon, workspace)
	if j, found, err := q.Claim(context.Background(), lease, []string{"foghorn"}); found || err != nil {
		t.Errorf("Claim of a kind none is queued of = %+v, %v, %v; want none", j, found, err)
	}
	if j, found, _ := q.Claim(context.Background(), lease, []string{"beacon"}); !found || j.ID != beacon {
		t.Errorf("Claim of beacons = %+v, %v; want the beacon, not the lamp due sooner", j, found)
	}
}

func TestClaimsAtOnceNeverTakeTheSameJob(t *testing.T) {
	q, pool := queueOf(t)
	const queued = 20
	for range queued {
		add(t, pool, workspace, time.Second)
	}
	var mu sync.Mutex
	taken := map[uuid.UUID]int{}
	var claims sync.WaitGroup
	for range 2 * queued {
		claims.Go(func() {
			j, found, err := q.Claim(context.Background(), lease, lamps)
			if err != nil {
				t.Errorf("Claim: %v", err)
			}
			if found {
				mu.Lock()
				taken[j.ID]++
				mu.Unlock()
			}
		})
	}
	claims.Wait()
	if len(taken) != queued {
		t.Errorf("%d jobs taken, want all %d", len(taken), queued)
	}
	for id, n := range taken {
		if n != 1 {
			t.Errorf("job %s taken %d times, want once", id, n)
		}
	}
}

func TestAClaimedJobIsDueAgainOnceItsLeaseRunsOut(t *testing.T) {
	q, pool := queueOf(t)
	id := add(t, pool, workspace, time.Second)
	first, _, _ := q.Claim(context.Background(), lease, lamps)
	if _, found, _ := q.Claim(context.Background(), lease, lamps); found {
		t.Fatal("a held job was claimed again")
	}
	pgtest.Exec(t, pool, "UPDATE jobs SET run_at = now() - interval '1 second' WHERE id = $1", id) // the lease ran out
	second, found, err := q.Claim(context.Background(), lease, lamps)
	if err != nil || !found || second.ID != id || second.Attempt != 2 {
		t.Fatalf("Claim after the lease = %+v, %v, %v; want the job on its second run", second, found, err)
	}
	if err := q.Done(context.Background(), first); err == nil {
		t.Error("the first run saved its result after the second claimed the job")
	}
	if err := q.Done(context.Background(), second); err != nil {
		t.Errorf("Done: %v", err)
	}
	if s, attempts, _ := state(t, pool, id); s != "done" || attempts != 2 {
		t.Errorf("job %s after %d runs, want done after 2", s, attempts)
	}
}

func TestARetriedJobIsDueAfterItsWaitAndAFailedOneNeverAgain(t *testing.T) {
	q, pool := queueOf(t)
	retried, failed := add(t, pool, workspace, time.Minute), add(t, pool, workspace, time.Second)
	r, _, _ := q.Claim(context.Background(), lease, lamps)
	f, _, _ := q.Claim(context.Background(), lease, lamps)
	if err := q.Retry(context.Background(), r, time.Hour, "the lamp won't light"); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if err := q.Fail(context.Background(), f, "no such tower"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if _, found, _ := q.Claim(context.Background(), lease, lamps); found {
		t.Error("a job was claimed before its wait, or after it failed")
	}
	pgtest.Exec(t, pool, "UPDATE jobs SET run_at = now() - interval '1 second' WHERE id = $1", retried) // the wait is over
	if again, found, _ := q.Claim(context.Background(), lease, lamps); !found || again.ID != retried {
		t.Errorf("Claim after the wait = %+v, %v; want the retried job", again, found)
	}
	if s, _, why := state(t, pool, retried); s != "waiting" || why != "the lamp won't light" {
		t.Errorf("retried job %s, %q; want waiting, with why it failed", s, why)
	}
	if s, _, why := state(t, pool, failed); s != "failed" || why != "no such tower" {
		t.Errorf("failed job %s, %q; want failed, with why", s, why)
	}
}

func TestAJobQueuedAfterAnotherIsClaimedOnlyOnceThatOneIsDoneOrFailed(t *testing.T) {
	for name, settle := range map[string]func(*postgres.Store, jobs.Job) error{
		"done": func(q *postgres.Store, j jobs.Job) error { return q.Done(context.Background(), j) },
		"failed": func(q *postgres.Store, j jobs.Job) error {
			return q.Fail(context.Background(), j, "the lamp won't light")
		},
	} {
		t.Run(name, func(t *testing.T) {
			q, pool := queueOf(t)
			first := add(t, pool, workspace, time.Second)
			after := add(t, pool, workspace, time.Minute) // due sooner than the job it waits for
			pgtest.Exec(t, pool, "UPDATE jobs SET after_id = $1 WHERE id = $2", first, after)

			j, _, _ := q.Claim(context.Background(), lease, lamps)
			if j.ID != first {
				t.Fatalf("Claim = %s, want the job the other waits for, %s", j.ID, first)
			}
			if err := q.Retry(context.Background(), j, 0, "the lamp flickers"); err != nil { // due again at once
				t.Fatalf("Retry: %v", err)
			}
			if again, _, _ := q.Claim(context.Background(), lease, lamps); again.ID != first {
				t.Fatalf("Claim while the first is retried = %s, want the first again, the other still waiting on it", again.ID)
			}
			if err := settle(q, jobs.Job{ID: first, Workspace: workspace, Run: 2}); err != nil {
				t.Fatalf("settle the first: %v", err)
			}
			if next, found, err := q.Claim(context.Background(), lease, lamps); err != nil || !found || next.ID != after {
				t.Errorf("Claim once the first settled = %+v, %v, %v; want the job queued after it", next, found, err)
			}
		})
	}
}

func TestARunCantSaveAnotherWorkspacesJob(t *testing.T) {
	q, pool := queueOf(t)
	id := add(t, pool, workspace, time.Second)
	j, _, _ := q.Claim(context.Background(), lease, lamps)
	j.Workspace = elsewhere
	if err := q.Fail(context.Background(), j, "not mine"); err == nil {
		t.Error("a run named with another workspace failed the job")
	}
	if s, _, _ := state(t, pool, id); s != "waiting" {
		t.Errorf("job %s, want still waiting", s)
	}
}

func TestAReleasedJobIsDueAtOnceWithItsRunNotCounted(t *testing.T) {
	q, pool := queueOf(t)
	id := add(t, pool, workspace, time.Second)
	j, _, _ := q.Claim(context.Background(), lease, lamps)
	if err := q.Release(context.Background(), j, 0); err != nil {
		t.Fatalf("Release: %v", err)
	}
	again, found, err := q.Claim(context.Background(), lease, lamps)
	if err != nil || !found || again.ID != id || again.Run != 2 || again.Attempt != 1 {
		t.Errorf("Claim after the release = %+v, %v, %v; want the job at once, its second run counted as its first attempt", again, found, err)
	}
}

func TestARunPausedPastItsLeaseCantSaveOnceALaterRunWasReleased(t *testing.T) {
	q, pool := queueOf(t)
	id := add(t, pool, workspace, time.Second)
	paused, _, _ := q.Claim(context.Background(), lease, lamps)
	pgtest.Exec(t, pool, "UPDATE jobs SET run_at = now() - interval '1 second' WHERE id = $1", id) // its lease ran out
	second, _, _ := q.Claim(context.Background(), lease, lamps)
	if err := q.Release(context.Background(), second, 0); err != nil { // stopped: its run not counted
		t.Fatalf("Release: %v", err)
	}
	third, _, _ := q.Claim(context.Background(), lease, lamps)
	if err := q.Done(context.Background(), paused); err == nil {
		t.Error("the paused first run saved its result, though a later run holds the job")
	}
	if err := q.Done(context.Background(), third); err != nil || third.Run != 3 || third.Attempt != 2 {
		t.Errorf("the third run = %+v, Done %v; want run 3, its second attempt, saving", third, err)
	}
}

func TestPruneDeletesOnlyTheKindsItIsGiven(t *testing.T) {
	q, pool := queueOf(t)
	lamp, beacon := add(t, pool, workspace, time.Second), uuid.NewV7()
	pgtest.Exec(t, pool, `INSERT INTO jobs (id, workspace_id, kind, args, state, finished_at)
		VALUES ($1, $2, 'beacon', '{}', 'done', now() - interval '8 days')`, beacon, workspace)
	pgtest.Exec(t, pool, "UPDATE jobs SET state = 'done', finished_at = now() - interval '8 days' WHERE id = $1", lamp)
	if n, err := q.Prune(context.Background(), 7*24*time.Hour, []string{"beacon"}); n != 1 || err != nil {
		t.Errorf("Prune of beacons = %d, %v; want the one beacon deleted", n, err)
	}
	if s, _, _ := state(t, pool, lamp); s != "done" {
		t.Errorf("the lamp done is %q, want it kept: its runner prunes it by its own age", s)
	}
}

func TestPruneDeletesOnlyJobsDoneLongEnoughAgo(t *testing.T) {
	q, pool := queueOf(t)
	old, recent, failed := add(t, pool, workspace, time.Second), add(t, pool, elsewhere, time.Second), add(t, pool, workspace, time.Second)
	for range 3 { // each is claimed and run: one fails, the others are done
		j, _, _ := q.Claim(context.Background(), lease, lamps)
		if j.ID == failed {
			_ = q.Fail(context.Background(), j, "no such tower")
		} else {
			_ = q.Done(context.Background(), j)
		}
	}
	pgtest.Exec(t, pool, "UPDATE jobs SET finished_at = now() - interval '8 days' WHERE id IN ($1, $2)", old, failed)
	if n, err := q.Prune(context.Background(), 7*24*time.Hour, lamps); n != 1 || err != nil {
		t.Errorf("Prune = %d, %v; want the one job done 8 days ago deleted", n, err)
	}
	var left int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM jobs WHERE id IN ($1, $2)", recent, failed).Scan(&left); err != nil || left != 2 {
		t.Errorf("%d of the recent and the failed job left, %v; want both kept", left, err)
	}
}

var _ jobs.Store = (*postgres.Store)(nil)
