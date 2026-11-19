// Package postgres keeps the job queue in PostgreSQL, in the jobs table. The
// queries are in queries.sql; sqlc generates the db package from them.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/jobs"
	"github.com/mikaclements/voice-guide/internal/jobs/store/postgres/db"
)

// Store keeps the job queue in PostgreSQL.
type Store struct {
	q *db.Queries
}

// New returns the store on pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{q: db.New(pool)}
}

// Claim holds the waiting job of one of kinds due soonest, for lease, in one statement.
func (s *Store) Claim(ctx context.Context, lease time.Duration, kinds []string) (jobs.Job, bool, error) {
	r, err := s.q.ClaimJob(ctx, db.ClaimJobParams{LeaseSeconds: lease.Seconds(), Kinds: kinds})
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, false, nil
	}
	if err != nil {
		return jobs.Job{}, false, fmt.Errorf("claim a job: %w", err)
	}
	j := jobs.Job{ID: r.ID, Workspace: r.WorkspaceID, Kind: r.Kind, Args: r.Args, Run: int(r.Runs), Attempt: int(r.Attempts)}
	if r.UserID.Valid {
		j.User = uuid.UUID(r.UserID.Bytes)
	}
	return j, true, nil
}

// errNotHeld reports a run's result saved after another run took the job.
var errNotHeld = errors.New("the job is no longer held by this run")

// Done marks the job finished, if this run holds it.
func (s *Store) Done(ctx context.Context, j jobs.Job) error {
	return held(s.q.FinishJob(ctx, db.FinishJobParams{ID: j.ID, WorkspaceID: j.Workspace, Run: run(j)}))
}

// Retry lets go of the job, due again after wait, if this run holds it.
func (s *Store) Retry(ctx context.Context, j jobs.Job, wait time.Duration, why string) error {
	return held(s.q.RetryJob(ctx, db.RetryJobParams{WaitSeconds: wait.Seconds(), Why: why, ID: j.ID, WorkspaceID: j.Workspace, Run: run(j)}))
}

// Fail ends the job for good, if this run holds it.
func (s *Store) Fail(ctx context.Context, j jobs.Job, why string) error {
	return held(s.q.FailJob(ctx, db.FailJobParams{Why: why, ID: j.ID, WorkspaceID: j.Workspace, Run: run(j)}))
}

// Release lets go of the job, due again after wait, without counting the run.
func (s *Store) Release(ctx context.Context, j jobs.Job, wait time.Duration) error {
	return held(s.q.ReleaseJob(ctx, db.ReleaseJobParams{WaitSeconds: wait.Seconds(), ID: j.ID, WorkspaceID: j.Workspace, Run: run(j)}))
}

// Prune deletes the jobs of kinds done at least age ago, in one statement.
func (s *Store) Prune(ctx context.Context, age time.Duration, kinds []string) (int, error) {
	n, err := s.q.PruneJobs(ctx, db.PruneJobsParams{AgeSeconds: age.Seconds(), Kinds: kinds})
	if err != nil {
		return 0, fmt.Errorf("delete old jobs: %w", err)
	}
	return int(n), nil
}

// run is the run's number as the column holds it; a number past the
// column's range is no run's, so it matches none.
func run(j jobs.Job) int32 {
	if j.Run < 0 || j.Run > math.MaxInt32 {
		return -1
	}
	return int32(j.Run)
}

func held(n int64, err error) error {
	switch {
	case err != nil:
		return fmt.Errorf("save the job's run: %w", err)
	case n != 1:
		return errNotHeld
	}
	return nil
}
