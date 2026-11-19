-- A user's sessions, newest first: ids are UUIDv7, so id order is start order, and a page of the
-- list reads from the cursor's place in the index instead of sorting every session of the user.

-- +goose Up
CREATE INDEX sessions_by_user ON sessions (workspace_id, user_id, id DESC);

-- +goose Down
DROP INDEX sessions_by_user;
