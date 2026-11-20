-- The model each agent version's summary is written on, and the schema of its structured part.

-- +goose Up
-- Empty is the agent's own model, and {} no schema, as every version saved before it used.
ALTER TABLE agent.agent_versions ADD COLUMN summary_model text NOT NULL DEFAULT '';
ALTER TABLE agent.agent_versions ADD COLUMN summary_schema jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE agent.agent_versions DROP COLUMN summary_schema;
ALTER TABLE agent.agent_versions DROP COLUMN summary_model;
