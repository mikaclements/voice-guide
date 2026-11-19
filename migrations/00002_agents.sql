-- The agents module's own schema, agent, so its tables can move with it: each workspace's agents, with
-- every version ever published.

-- +goose Up
CREATE SCHEMA agent;

CREATE TABLE agent.agents (
    id           uuid PRIMARY KEY CHECK (uuid_extract_version(id) = 7),
    workspace_id uuid NOT NULL REFERENCES workspaces (id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- Lets a version's foreign key check that it belongs to its agent's workspace.
    UNIQUE (id, workspace_id)
);

CREATE TABLE agent.agent_versions (
    workspace_id uuid NOT NULL,
    agent_id     uuid NOT NULL,
    version      integer NOT NULL CHECK (version > 0),
    name         text NOT NULL,
    description  text NOT NULL,
    reads        text[] NOT NULL,
    persona      text NOT NULL,
    -- Publishing compares this with the newest version's, so the same text saves no new version.
    content_hash text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, version),
    FOREIGN KEY (agent_id, workspace_id) REFERENCES agent.agents (id, workspace_id)
);

-- A saved version is never changed or removed: running sessions depend on it staying as it was.
-- +goose StatementBegin
CREATE FUNCTION agent.refuse_version_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'agent versions are never changed or deleted'
        USING ERRCODE = 'restrict_violation';
END
$$;
-- +goose StatementEnd

CREATE TRIGGER agent_versions_never_change
    BEFORE UPDATE OR DELETE ON agent.agent_versions
    FOR EACH ROW EXECUTE FUNCTION agent.refuse_version_change();

CREATE TRIGGER agent_versions_never_truncated
    BEFORE TRUNCATE ON agent.agent_versions
    FOR EACH STATEMENT EXECUTE FUNCTION agent.refuse_version_change();

-- Even a session that turns ordinary triggers off for replication still meets these two.
ALTER TABLE agent.agent_versions ENABLE ALWAYS TRIGGER agent_versions_never_change;
ALTER TABLE agent.agent_versions ENABLE ALWAYS TRIGGER agent_versions_never_truncated;

-- +goose Down
DROP SCHEMA agent CASCADE;
