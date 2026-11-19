-- The column holding a session's session context takes the term's full name, so it can't be read
-- as the user context the session loaded, which sits beside it in context_item_ids.

-- +goose Up
ALTER TABLE sessions RENAME COLUMN context TO session_context;

-- +goose Down
ALTER TABLE sessions RENAME COLUMN session_context TO context;
