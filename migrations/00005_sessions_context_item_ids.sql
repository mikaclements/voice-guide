-- The context items a session loaded about its user, by ID and in order. Every turn rebuilds the
-- prompt from them, so any instance builds the same one; their text stays in user context.

-- +goose Up
ALTER TABLE sessions ADD COLUMN context_item_ids uuid[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE sessions DROP COLUMN context_item_ids;
