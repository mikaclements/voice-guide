-- The model each agent version's summary is written on, and what its sessions are graded on.

-- +goose Up
-- Empty is the agent's own model, and {} grades nothing, as every version saved before it used.
ALTER TABLE agent.agent_versions ADD COLUMN summary_model text NOT NULL DEFAULT '';
ALTER TABLE agent.agent_versions ADD COLUMN grading jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE agent.agent_versions DROP COLUMN grading;
ALTER TABLE agent.agent_versions DROP COLUMN summary_model;
