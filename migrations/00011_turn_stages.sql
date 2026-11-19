-- Turn stages: how long each stage of a turn took, and what served it.

-- +goose Up
-- The key a stage points at, so a stage's workspace and user are always its turn's.
ALTER TABLE turns ADD UNIQUE (id, workspace_id, user_id);

CREATE TABLE turn_stages (
    turn_id        uuid NOT NULL,
    workspace_id   uuid NOT NULL,
    user_id        uuid NOT NULL,
    -- A new stage is a migration, so every stage a turn records is one this list knows.
    stage          text NOT NULL CHECK (stage IN ('prompt', 'model_first_text', 'model_reply', 'save')),
    -- What served the stage, such as claude-subscription/claude-sonnet-5-5; empty for voice's own work.
    implementation text NOT NULL DEFAULT '',
    started_at     timestamptz NOT NULL,
    duration_ms    integer NOT NULL CHECK (duration_ms >= 0),
    -- What else the stage reported, such as a reply's token counts.
    details        jsonb NOT NULL DEFAULT '{}',
    PRIMARY KEY (turn_id, stage),
    FOREIGN KEY (turn_id, workspace_id, user_id) REFERENCES turns (id, workspace_id, user_id)
);

-- +goose Down
DROP TABLE turn_stages;
ALTER TABLE turns DROP CONSTRAINT turns_id_workspace_id_user_id_key;
