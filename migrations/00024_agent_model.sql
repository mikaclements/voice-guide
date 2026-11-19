-- The model each agent version asks for.

-- +goose Up
-- Empty is the server's own model, as every version saved before it used.
ALTER TABLE agent.agent_versions ADD COLUMN model text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agent.agent_versions DROP COLUMN model;
