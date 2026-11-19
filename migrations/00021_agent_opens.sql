-- Whether each agent version speaks first in its sessions.

-- +goose Up
-- False waits for the user, as every version saved before it did.
ALTER TABLE agent.agent_versions ADD COLUMN opens boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE agent.agent_versions DROP COLUMN opens;
