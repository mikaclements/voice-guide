-- Session summaries, each kept with the agent that wrote it and the session it is of.

-- +goose Up
-- A session summary is read back only by the agent that wrote it, newest session first. No summary
-- was written before this, so any here are sample data, which no agent could read.
DELETE FROM user_context.context_items WHERE kind = 'session_summary';
ALTER TABLE user_context.context_items
    ADD COLUMN agent_id uuid,
    -- When the session it is of ended, so summaries order by their sessions, not by when each was
    -- saved; a correction keeps its original's.
    ADD COLUMN source_ended_at timestamptz,
    ADD CONSTRAINT context_items_summary_of_an_agents_session CHECK (
        (kind = 'session_summary') = (agent_id IS NOT NULL AND source_session_id IS NOT NULL AND source_ended_at IS NOT NULL));

-- One summary per session however many times its job runs; a correction replaces it as any item is
-- replaced, so only the first is held to one.
CREATE UNIQUE INDEX context_items_one_summary_per_session
    ON user_context.context_items (workspace_id, source_session_id) WHERE kind = 'session_summary' AND replaces IS NULL;

-- +goose Down
DROP INDEX user_context.context_items_one_summary_per_session;
ALTER TABLE user_context.context_items
    DROP CONSTRAINT context_items_summary_of_an_agents_session, DROP COLUMN source_ended_at, DROP COLUMN agent_id;
