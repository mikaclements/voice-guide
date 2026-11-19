-- The documents only each agent version's summary reads, kept from its conversation's prompt.

-- +goose Up
-- Empty reads none, as every version saved before it.
ALTER TABLE agent.agent_versions ADD COLUMN summary_documents text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE agent.agent_versions DROP COLUMN summary_documents;
