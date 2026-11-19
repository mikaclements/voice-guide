-- User context's kinds: only what voice-guide itself holds about a user.

-- +goose Up
-- A resume is what a product already knows about the user, so it arrives as session context; user
-- context keeps what voice-guide itself holds. Items of the dropped kind were sample data only.
DELETE FROM user_context.context_items WHERE kind = 'resume';
ALTER TABLE user_context.context_items DROP CONSTRAINT context_items_kind_check;
ALTER TABLE user_context.context_items
    ADD CONSTRAINT context_items_kind_check CHECK (kind IN ('profile', 'session_summary'));

-- +goose Down
ALTER TABLE user_context.context_items DROP CONSTRAINT context_items_kind_check;
ALTER TABLE user_context.context_items
    ADD CONSTRAINT context_items_kind_check CHECK (kind IN ('resume', 'profile', 'session_summary'));
