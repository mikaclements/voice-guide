-- Every query names the workspace, so one workspace never reads or writes another's agents.

-- name: AddAgent :exec
INSERT INTO agent.agents (id, workspace_id)
VALUES (@id, @workspace_id)
ON CONFLICT (id) DO NOTHING;

-- Held until the transaction ends, so two publishes of one agent take turns choosing a number.
-- name: LockAgent :one
SELECT id FROM agent.agents
WHERE id = @id AND workspace_id = @workspace_id
FOR UPDATE;

-- name: NewestVersionOf :one
SELECT version, content_hash FROM agent.agent_versions
WHERE workspace_id = @workspace_id AND agent_id = @agent_id
ORDER BY version DESC
LIMIT 1;

-- name: AddVersion :exec
INSERT INTO agent.agent_versions
    (workspace_id, agent_id, version, name, description, reads, session_context, persona, summary, summary_model, grading, opens, model, voice, speed, content_hash)
VALUES
    (@workspace_id, @agent_id, @version, @name, @description, @reads, @session_context, @persona, @summary, @summary_model, @grading, @opens, @model, @voice, @speed, @content_hash);

-- name: NewestVersions :many
SELECT newest.agent_id, newest.version, newest.name, newest.description, newest.reads, newest.session_context, newest.persona, newest.summary, newest.summary_model, newest.grading, newest.opens, newest.model, newest.voice, newest.speed
FROM (
    SELECT DISTINCT ON (agent_id) agent_id, version, name, description, reads, session_context, persona, summary, summary_model, grading, opens, model, voice, speed
    FROM agent.agent_versions
    WHERE workspace_id = @workspace_id
    ORDER BY agent_id, version DESC
) AS newest
ORDER BY newest.name, newest.agent_id;

-- name: Version :one
SELECT agent_id, version, name, description, reads, session_context, persona, summary, summary_model, grading, opens, model, voice, speed
FROM agent.agent_versions
WHERE workspace_id = @workspace_id AND agent_id = @agent_id AND version = @version;
