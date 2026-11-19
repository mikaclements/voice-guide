-- User context's own schema, so its tables can move with it: what it holds about each user.

-- +goose Up
CREATE SCHEMA user_context;

CREATE TABLE user_context.context_items (
    id                uuid PRIMARY KEY CHECK (uuid_extract_version(id) = 7),
    workspace_id      uuid NOT NULL REFERENCES workspaces (id),
    user_id           uuid NOT NULL REFERENCES users (id),
    -- A new kind is a migration, so an agent can't declare a kind nothing writes.
    kind              text NOT NULL CHECK (kind IN ('resume', 'profile', 'session_summary')),
    content           text NOT NULL CHECK (content <> ''),
    -- An item is never edited: a change is a new item replacing the old one, so a session can tell
    -- that what it loaded is no longer current. Only one item replaces another, and only an item of
    -- the same user, workspace and kind.
    replaces          uuid UNIQUE,
    -- The session that wrote it, for summaries; sessions are voice's, so no reference.
    source_session_id uuid,
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, workspace_id, user_id, kind),
    FOREIGN KEY (replaces, workspace_id, user_id, kind)
        REFERENCES user_context.context_items (id, workspace_id, user_id, kind)
);

-- Loading reads one user's items of some kinds, newest first.
CREATE INDEX context_items_by_user_kind
    ON user_context.context_items (workspace_id, user_id, kind, created_at DESC);

-- +goose StatementBegin
CREATE FUNCTION user_context.refuse_item_edit() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'context items are never edited; save a new item that replaces it'
        USING ERRCODE = 'restrict_violation';
END
$$;
-- +goose StatementEnd

-- Deleting stays possible: a user may remove what voice-guide knows about them.
CREATE TRIGGER context_items_never_edited
    BEFORE UPDATE ON user_context.context_items
    FOR EACH ROW EXECUTE FUNCTION user_context.refuse_item_edit();
ALTER TABLE user_context.context_items ENABLE ALWAYS TRIGGER context_items_never_edited;

-- +goose Down
DROP SCHEMA user_context CASCADE;
