-- User references: the name a product gives each of its users, mapped to a voice-guide user.

-- +goose Up
-- A product names its users in its own words; voice-guide adds a user with the first session a
-- product starts for them, and the product never sees voice-guide's user IDs.
CREATE TABLE user_references (
    workspace_id uuid NOT NULL REFERENCES workspaces (id),
    reference    text NOT NULL CHECK (char_length(reference) BETWEEN 1 AND 200),
    -- Checked at the commit, so a start writes the reference before the user: one that finds the
    -- reference already added adds no user of its own.
    user_id      uuid NOT NULL REFERENCES users (id) DEFERRABLE INITIALLY DEFERRED,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, reference),
    -- One reference per user in a workspace, so a user's reference can be read back from them.
    UNIQUE (workspace_id, user_id)
);

-- +goose Down
DROP TABLE user_references;
