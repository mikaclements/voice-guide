-- The voice and speed each agent version asks for, and the voice and speed each session speaks in.

-- +goose Up
-- Empty and 0 are the server's own, as every version and session saved before it used.
ALTER TABLE agent.agent_versions ADD COLUMN voice text NOT NULL DEFAULT '';
ALTER TABLE agent.agent_versions ADD COLUMN speed double precision NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN voice text NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN speed double precision NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sessions DROP COLUMN speed;
ALTER TABLE sessions DROP COLUMN voice;
ALTER TABLE agent.agent_versions DROP COLUMN speed;
ALTER TABLE agent.agent_versions DROP COLUMN voice;
