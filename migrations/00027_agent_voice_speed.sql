-- The voice and speed each agent version asks for.

-- +goose Up
-- Empty and 0 are the server's own, as every version saved before it used.
ALTER TABLE agent.agent_versions ADD COLUMN voice text NOT NULL DEFAULT '';
ALTER TABLE agent.agent_versions ADD COLUMN speed double precision NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE agent.agent_versions DROP COLUMN speed;
ALTER TABLE agent.agent_versions DROP COLUMN voice;
