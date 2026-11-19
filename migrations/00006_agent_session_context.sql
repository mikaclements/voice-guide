-- What a product must send when a session with the agent's version starts, by name. A saved
-- version never changes, so this is fixed with the version, like the kinds it reads.

-- +goose Up
ALTER TABLE agent.agent_versions ADD COLUMN session_context text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE agent.agent_versions DROP COLUMN session_context;
