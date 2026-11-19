-- The queue serves every workspace: claiming and deleting old jobs find their rows across
-- workspaces by one condition, and each job then works within its own workspace.

-- The claim moves the job's run_at to the end of its lease, so another claim finds it due again only
-- once the lease has run out, as when its instance stopped. SKIP LOCKED passes over a job another
-- claim is taking, so two claims at once never take the same job. A claim takes only the kinds its
-- runner has handlers for, so an instance never takes a job it can't run, such as a new kind during a
-- rolling deploy. A job queued to run after another waits while that one does, retries included.
-- name: ClaimJob :one
UPDATE jobs SET runs = runs + 1, attempts = attempts + 1, run_at = now() + make_interval(secs => @lease_seconds::float8)
WHERE id = (
    SELECT j.id FROM jobs j
    WHERE j.state = 'waiting' AND j.run_at <= now() AND j.kind = ANY(@kinds::text[])
      AND NOT EXISTS (SELECT FROM jobs prior WHERE prior.id = j.after_id AND prior.state = 'waiting')
    ORDER BY j.run_at, j.id
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING id, workspace_id, user_id, kind, args, runs, attempts;

-- A run's result is saved only by the run that holds the job: the latest claimed, by its number.
-- name: FinishJob :execrows
UPDATE jobs SET state = 'done', finished_at = now()
WHERE id = @id AND workspace_id = @workspace_id AND runs = @run AND state = 'waiting';

-- name: RetryJob :execrows
UPDATE jobs SET run_at = now() + make_interval(secs => @wait_seconds::float8), last_error = @why::text
WHERE id = @id AND workspace_id = @workspace_id AND runs = @run AND state = 'waiting';

-- A run cut short by a stop, or let go because what it needed was busy, doesn't count against the
-- job; its number stays used.
-- name: ReleaseJob :execrows
UPDATE jobs SET run_at = now() + make_interval(secs => @wait_seconds::float8), attempts = attempts - 1
WHERE id = @id AND workspace_id = @workspace_id AND runs = @run AND state = 'waiting';

-- Jobs done are deleted once old, each runner its own kinds by its own age; failed ones stay, to be
-- looked into.
-- name: PruneJobs :execrows
DELETE FROM jobs
WHERE state = 'done' AND kind = ANY(@kinds::text[]) AND finished_at <= now() - make_interval(secs => @age_seconds::float8);

-- name: FailJob :execrows
UPDATE jobs SET state = 'failed', finished_at = now(), last_error = @why::text
WHERE id = @id AND workspace_id = @workspace_id AND runs = @run AND state = 'waiting';
