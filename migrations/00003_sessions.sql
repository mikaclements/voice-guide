-- Users, and the sessions they start with an agent version. Voice's records, in the default schema.

-- +goose Up
CREATE TABLE users (
    -- One account per human across workspaces, so no workspace here. Added the first time a
    -- valid token names the user; nothing else about them is kept yet.
    id         uuid PRIMARY KEY CHECK (uuid_extract_version(id) = 7),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id            uuid PRIMARY KEY CHECK (uuid_extract_version(id) = 7),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id),
    user_id       uuid NOT NULL REFERENCES users (id),
    -- The exact version the session started on; versions never change, so neither does the agent.
    agent_id      uuid NOT NULL,
    agent_version integer NOT NULL,
    -- Chosen by the caller, so pressing start twice makes one session; per user, so no one can
    -- reach another user's session by sending their request ID.
    request_id    uuid NOT NULL,
    started_at    timestamptz NOT NULL DEFAULT now(),
    ended_at      timestamptz,
    UNIQUE (workspace_id, user_id, request_id),
    FOREIGN KEY (agent_id, agent_version) REFERENCES agent.agent_versions (agent_id, version),
    -- The agent must be the session's workspace's.
    FOREIGN KEY (agent_id, workspace_id) REFERENCES agent.agents (id, workspace_id)
);

-- +goose Down
DROP TABLE sessions;
DROP TABLE users;
