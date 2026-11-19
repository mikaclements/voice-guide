-- Turns: one exchange of a session each, the user's words and screen input and the agent's reply.

-- +goose Up
-- The key a turn points at, so a turn's workspace and user are always its session's.
ALTER TABLE sessions ADD UNIQUE (id, workspace_id, user_id);

CREATE TABLE turns (
    id           uuid PRIMARY KEY CHECK (uuid_extract_version(id) = 7),
    workspace_id uuid NOT NULL,
    user_id      uuid NOT NULL,
    session_id   uuid NOT NULL,
    -- Chosen by the caller, so a turn sent twice can be recognised; any UUID version.
    request_id   uuid NOT NULL,
    -- 1, 2, 3 within the session: the order is explicit, and two turns at once can't both take the next.
    seq          integer NOT NULL CHECK (seq >= 1),
    user_text    text NOT NULL,
    -- The screen input sent with the turn, as named text blocks in order.
    screen_input jsonb NOT NULL DEFAULT '[]',
    -- Exactly as the model sent it, so later turns show the model its own words.
    agent_text   text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'in_flight' CHECK (status IN ('in_flight', 'answered', 'lost', 'cut')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz CHECK ((status = 'in_flight') = (finished_at IS NULL)),
    UNIQUE (session_id, seq),
    UNIQUE (session_id, request_id),
    FOREIGN KEY (session_id, workspace_id, user_id) REFERENCES sessions (id, workspace_id, user_id)
);

-- +goose Down
DROP TABLE turns;
ALTER TABLE sessions DROP CONSTRAINT sessions_id_workspace_id_user_id_key;
