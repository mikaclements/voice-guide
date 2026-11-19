-- Every query about context items names the workspace and the user, so one workspace never reads
-- another's, and no user reads another's.

-- An item replaced by a newer one is no longer current, so loading never shows it. A session summary
-- is read only by the agent that wrote it, and ordered by when its session ended, so the latest
-- session's comes first even when an older one's was saved later. At most @max_rows of each kind,
-- the newest, are read, so a start never reads every summary a user ever had.
-- name: CurrentItems :many
SELECT c.id, c.workspace_id, c.user_id, c.kind, c.content, c.replaces, c.agent_id, c.source_session_id, c.source_ended_at, c.created_at
FROM (
    SELECT i.id, i.workspace_id, i.user_id, i.kind, i.content, i.replaces, i.agent_id, i.source_session_id, i.source_ended_at, i.created_at,
           row_number() OVER (PARTITION BY i.kind ORDER BY coalesce(i.source_ended_at, i.created_at) DESC, i.id DESC) AS place
    FROM user_context.context_items i
    WHERE i.workspace_id = @workspace_id AND i.user_id = @user_id AND i.kind = ANY(@kinds::text[])
      AND (i.kind <> 'session_summary' OR i.agent_id = @agent_id::uuid)
      AND NOT EXISTS (
          SELECT 1 FROM user_context.context_items newer
          WHERE newer.replaces = i.id AND newer.workspace_id = @workspace_id
      )
) c
WHERE c.place <= @max_rows::integer
ORDER BY coalesce(c.source_ended_at, c.created_at) DESC, c.id DESC;

-- A session reads back the items it loaded, by primary key, even those replaced since.
-- name: ItemsByID :many
SELECT id, workspace_id, user_id, kind, content, replaces, agent_id, source_session_id, source_ended_at, created_at
FROM user_context.context_items
WHERE workspace_id = @workspace_id AND user_id = @user_id AND id = ANY(@ids::uuid[]);

-- A session has one summary: a second save, as a job run twice makes, inserts nothing, and the
-- caller then reads the first. A correction, which replaces it, isn't held to this.
-- name: AddSummary :one
INSERT INTO user_context.context_items (id, workspace_id, user_id, kind, content, agent_id, source_session_id, source_ended_at)
VALUES (@id, @workspace_id, @user_id, 'session_summary', @content, @agent_id::uuid, @source_session_id::uuid, @source_ended_at::timestamptz)
ON CONFLICT (workspace_id, source_session_id) WHERE kind = 'session_summary' AND replaces IS NULL DO NOTHING
RETURNING id, workspace_id, user_id, kind, content, replaces, agent_id, source_session_id, source_ended_at, created_at;

-- name: SummaryOfSession :one
SELECT id, workspace_id, user_id, kind, content, replaces, agent_id, source_session_id, source_ended_at, created_at
FROM user_context.context_items
WHERE workspace_id = @workspace_id AND user_id = @user_id AND kind = 'session_summary'
  AND source_session_id = @source_session_id::uuid AND replaces IS NULL;

-- The session's summary as it stands: the one saved, or the correction that replaced it.
-- name: CurrentSummaryOfSession :one
SELECT i.id, i.workspace_id, i.user_id, i.kind, i.content, i.replaces, i.agent_id, i.source_session_id, i.source_ended_at, i.created_at
FROM user_context.context_items i
WHERE i.workspace_id = @workspace_id AND i.user_id = @user_id AND i.kind = 'session_summary'
  AND i.source_session_id = @source_session_id::uuid
  AND NOT EXISTS (
      SELECT 1 FROM user_context.context_items newer
      WHERE newer.replaces = i.id AND newer.workspace_id = @workspace_id
  )
ORDER BY i.created_at DESC, i.id DESC
LIMIT 1;
