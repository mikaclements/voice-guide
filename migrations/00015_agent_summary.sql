-- Each agent version's summary instruction.

-- +goose Up
-- What an agent asks to be noted when one of its sessions ends; empty asks for no summary.
ALTER TABLE agent.agent_versions ADD COLUMN summary text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agent.agent_versions DROP COLUMN summary;
