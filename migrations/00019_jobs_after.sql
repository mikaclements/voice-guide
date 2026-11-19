-- A job that waits for another to settle before it runs.

-- +goose Up
-- The job this one runs after: it is claimed only once that one is done or failed, so a job queued
-- with another in the same write runs after it, however often the other is retried. Not a foreign
-- key: a job done is deleted after days, and one gone is waited on no longer.
ALTER TABLE jobs ADD COLUMN after_id uuid;

-- +goose Down
ALTER TABLE jobs DROP COLUMN after_id;
