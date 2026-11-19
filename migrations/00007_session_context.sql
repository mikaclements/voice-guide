-- The session context the product sent when the session started (a resume, a plan), as named text
-- blocks in order. Kept for the whole session, so any instance builds the same prompt on every turn.

-- +goose Up
ALTER TABLE sessions ADD COLUMN context jsonb NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE sessions DROP COLUMN context;
