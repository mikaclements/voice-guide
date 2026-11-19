-- Background jobs: one queue for every kind, owned by the job runner.

-- +goose Up
-- Its columns are only the queue's own: each kind's inputs are in args, read by that kind's handler
-- alone, so a new kind adds no column, and the data a job works on stays in its module's own
-- tables. A module queues a job by inserting its row, the kind and args in the shape its handler
-- reads, in the same transaction as the write that calls for it, so the job exists exactly when
-- that commits; nothing else outside the job runner writes here.
CREATE TABLE jobs (
    id           uuid PRIMARY KEY CHECK (uuid_extract_version(id) = 7),
    workspace_id uuid NOT NULL REFERENCES workspaces (id),
    -- The user the job is about, if it is about one.
    user_id      uuid REFERENCES users (id),
    kind         text NOT NULL CHECK (kind <> ''),
    args         jsonb NOT NULL,
    state        text NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'done', 'failed')),
    -- When a waiting job may next be claimed: at once when queued, after a wait when it failed, and
    -- once its lease runs out while it is being run, so a job whose instance stopped is run again.
    run_at       timestamptz NOT NULL DEFAULT now(),
    -- Runs started, numbered at each claim and never lowered: a run's result is saved only by the
    -- run of that number, so a run paused past its lease can't save over a later one.
    runs         integer NOT NULL DEFAULT 0 CHECK (runs >= 0),
    -- Runs counted against the job: a run let go because the runner stopped, or because what it
    -- needed was busy, isn't.
    attempts     integer NOT NULL DEFAULT 0 CHECK (attempts >= 0 AND attempts <= runs),
    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz CHECK ((state = 'waiting') = (finished_at IS NULL))
);

-- Claiming reads the waiting jobs that are due, oldest first.
CREATE INDEX jobs_due ON jobs (run_at) WHERE state = 'waiting';
-- Jobs done are deleted once old; failed ones stay, to be looked into.
CREATE INDEX jobs_done ON jobs (finished_at) WHERE state = 'done';

-- +goose Down
DROP TABLE jobs;
