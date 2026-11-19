-- When each session was last active, so one idle long enough ends.

-- +goose Up
-- The last time a turn started in the session or the user's speech was heard in it, or when it
-- started. Kept on the session's row, so a turn starting, a session ending and the sweep that ends
-- idle sessions all take the row's lock and see each other's writes.
ALTER TABLE sessions ADD COLUMN last_active_at timestamptz NOT NULL DEFAULT now();
UPDATE sessions SET last_active_at = greatest(started_at, (SELECT max(created_at) FROM turns WHERE turns.session_id = sessions.id));

-- Each instance looks every minute for live sessions idle long enough to end; ended sessions, most
-- of them in time, are left out of the index.
CREATE INDEX sessions_live ON sessions (last_active_at) WHERE ended_at IS NULL;

-- +goose Down
DROP INDEX sessions_live;
ALTER TABLE sessions DROP COLUMN last_active_at;
