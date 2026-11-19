-- Every query about sessions names the workspace, so one workspace never reads or writes another's,
-- but the sweep that ends idle sessions, which acts on every workspace alike.

-- A user is added with the first session a product starts for its reference; one account spans
-- workspaces.
-- name: AddUser :exec
INSERT INTO users (id) VALUES (@id) ON CONFLICT (id) DO NOTHING;

-- name: UserByReference :one
SELECT user_id FROM user_references WHERE workspace_id = @workspace_id AND reference = @reference;

-- A user has at most one reference in a workspace, so it reads back from them.
-- name: UserReference :one
SELECT reference FROM user_references WHERE workspace_id = @workspace_id AND user_id = @user_id;

-- A reference another start added meanwhile adds nothing; the caller then reads that one's user.
-- name: AddUserReference :one
INSERT INTO user_references (workspace_id, reference, user_id) VALUES (@workspace_id, @reference, @user_id)
ON CONFLICT (workspace_id, reference) DO NOTHING
RETURNING user_id;

-- A repeat of the same request inserts nothing; the caller then reads the session it made.
-- name: AddSession :one
INSERT INTO sessions (id, workspace_id, user_id, agent_id, agent_version, request_id, context_item_ids, session_context, voice, speed)
VALUES (@id, @workspace_id, @user_id, @agent_id, @agent_version, @request_id, @context_item_ids, @session_context, @voice, @speed)
ON CONFLICT (workspace_id, user_id, request_id) DO NOTHING
RETURNING id, workspace_id, user_id, agent_id, agent_version, context_item_ids, session_context, voice, speed, started_at, ended_at, summary_status;

-- name: SessionForRequest :one
SELECT id, workspace_id, user_id, agent_id, agent_version, context_item_ids, session_context, voice, speed, started_at, ended_at, summary_status FROM sessions
WHERE workspace_id = @workspace_id AND user_id = @user_id AND request_id = @request_id;

-- name: SessionByID :one
SELECT id, workspace_id, user_id, agent_id, agent_version, context_item_ids, session_context, voice, speed, started_at, ended_at, summary_status FROM sessions
WHERE workspace_id = @workspace_id AND user_id = @user_id AND id = @id;

-- A product's server names no user, so its reads find a session by its workspace alone.
-- name: SessionInWorkspace :one
SELECT id, workspace_id, user_id, agent_id, agent_version, context_item_ids, session_context, voice, speed, started_at, ended_at, summary_status FROM sessions
WHERE workspace_id = @workspace_id AND id = @id;

-- A page of the user's sessions, newest first: ids are UUIDv7, so id order is start order, and
-- @before, the last id of the page before, is where this one starts.
-- name: SessionsOfUser :many
SELECT id, workspace_id, user_id, agent_id, agent_version, context_item_ids, session_context, voice, speed, started_at, ended_at, summary_status FROM sessions
WHERE workspace_id = @workspace_id AND user_id = @user_id AND id < @before
ORDER BY id DESC
LIMIT @page_size;

-- Ending marks the summary pending and, in the same statement, queues the job that writes it, so the job
-- exists exactly when the end does; the job that settles the summary's status once the summary's job is
-- done or failed, so no session stays pending; and, when the workspace has a webhook not stopped, the
-- job that delivers the session to it, run once the summary's job is done or failed, with a delivery ID
-- kept on every attempt. Ending again changes nothing and queues nothing, so a retried end gets the same
-- answer. It waits for a turn starting in the session, which holds the session's row while it starts,
-- and a turn waiting on it then finds the session ended. It returns how many sessions it ended: one, or
-- none when the session had ended already or isn't the user's.
-- name: EndSession :one
WITH ended AS (
    UPDATE sessions SET ended_at = now(), summary_status = 'pending'
    WHERE sessions.workspace_id = @workspace_id AND sessions.user_id = @user_id AND sessions.id = @id AND sessions.ended_at IS NULL
    RETURNING sessions.id, sessions.workspace_id, sessions.user_id
), summaries AS (
    INSERT INTO jobs (id, workspace_id, user_id, kind, args)
    SELECT uuidv7(), ended.workspace_id, ended.user_id, 'session_summary', jsonb_build_object('session_id', ended.id) FROM ended
    RETURNING jobs.id, jobs.workspace_id, jobs.user_id, jobs.args
), settles AS (
    INSERT INTO jobs (id, workspace_id, user_id, kind, args, after_id)
    SELECT uuidv7(), summaries.workspace_id, summaries.user_id, 'session_summary_settle', summaries.args, summaries.id FROM summaries
), deliveries AS (
    INSERT INTO jobs (id, workspace_id, user_id, kind, args, after_id)
    SELECT uuidv7(), summaries.workspace_id, summaries.user_id, 'session_delivery',
           jsonb_build_object('session_id', summaries.args -> 'session_id', 'delivery_id', uuidv7()), summaries.id
    FROM summaries JOIN webhooks ON webhooks.workspace_id = summaries.workspace_id AND webhooks.stopped_at IS NULL
)
SELECT count(*) FROM ended;

-- A session not active for the idle time ends. This sweep, run by every instance, finds its rows by
-- one condition across workspaces, as the system's own sweeps do, ending each workspace's idle
-- sessions alike and reading nothing out. A session whose row a turn starting holds is checked again once the turn has
-- marked it active, so the sweep never ends a session under a turn that just started; two sweeps at
-- once end a session once, the second finding it ended when the first commits.
-- Each session ended queues its jobs in the same statement, as EndSession does, each delivery to its
-- own workspace's webhook.
-- name: EndIdleSessions :many
WITH ended AS (
    UPDATE sessions SET ended_at = now(), summary_status = 'pending'
    WHERE sessions.ended_at IS NULL AND sessions.last_active_at <= now() - make_interval(secs => @idle_seconds::float8)
    RETURNING sessions.id, sessions.workspace_id, sessions.user_id
), summaries AS (
    INSERT INTO jobs (id, workspace_id, user_id, kind, args)
    SELECT uuidv7(), ended.workspace_id, ended.user_id, 'session_summary', jsonb_build_object('session_id', ended.id) FROM ended
    RETURNING jobs.id, jobs.workspace_id, jobs.user_id, jobs.args
), settles AS (
    INSERT INTO jobs (id, workspace_id, user_id, kind, args, after_id)
    SELECT uuidv7(), summaries.workspace_id, summaries.user_id, 'session_summary_settle', summaries.args, summaries.id FROM summaries
), deliveries AS (
    INSERT INTO jobs (id, workspace_id, user_id, kind, args, after_id)
    SELECT uuidv7(), summaries.workspace_id, summaries.user_id, 'session_delivery',
           jsonb_build_object('session_id', summaries.args -> 'session_id', 'delivery_id', uuidv7()), summaries.id
    FROM summaries JOIN webhooks ON webhooks.workspace_id = summaries.workspace_id AND webhooks.stopped_at IS NULL
)
SELECT ended.id FROM ended;

-- A summary's status moves once, from pending to written or none, and never back.
-- name: SettleSummary :exec
UPDATE sessions SET summary_status = @status::text
WHERE workspace_id = @workspace_id AND user_id = @user_id AND id = @id AND summary_status = 'pending';

-- The user's speech was heard in the live session.
-- name: TouchSession :execrows
UPDATE sessions SET last_active_at = now()
WHERE workspace_id = @workspace_id AND user_id = @user_id AND id = @id AND ended_at IS NULL;

-- name: TurnForRequest :one
SELECT status FROM turns WHERE workspace_id = @workspace_id AND session_id = @session_id AND request_id = @request_id;

-- name: SessionEnded :one
SELECT (ended_at IS NOT NULL)::boolean AS ended FROM sessions WHERE workspace_id = @workspace_id AND id = @id;

-- The turn takes the number after the session's last. It is refused while another is in flight and
-- younger than the deadline, and once a turn after the one its prompt was built on (@after) has
-- finished, so no turn is answered without the turn before it, and once the session has ended. One in
-- flight past the deadline was cut off, by a crash say: it is saved lost here, so its words reach
-- later prompts. An opening is refused once the session has any turn at all, so it is only ever turn 1.
-- Two at once both read the same last number, and the unique key refuses the second.
-- Starting marks the session active, holding its row until the turn has started, so an end or the
-- idle sweep waits for the turn and then sees it, and a turn waiting on an end finds the session
-- ended: a turn is in flight when the end lands, or refused.
-- name: StartTurn :one
WITH live AS (
    UPDATE sessions SET last_active_at = now()
    WHERE workspace_id = @workspace_id AND id = @session_id AND ended_at IS NULL
    RETURNING id
), cut_off AS (
    UPDATE turns SET status = 'lost', finished_at = now()
    WHERE workspace_id = @workspace_id AND session_id = @session_id AND status = 'in_flight'
      AND created_at <= now() - make_interval(secs => @deadline_seconds::float8)
)
INSERT INTO turns (id, workspace_id, user_id, session_id, request_id, seq, user_text, screen_input, opening)
SELECT @id::uuid, @workspace_id::uuid, @user_id::uuid, @session_id::uuid, @request_id::uuid, coalesce(max(seq), 0) + 1,
       @user_text::text, @screen_input::jsonb, @opening::boolean
FROM turns WHERE workspace_id = @workspace_id AND session_id = @session_id
HAVING NOT coalesce(bool_or(CASE WHEN status = 'in_flight' THEN created_at > now() - make_interval(secs => @deadline_seconds::float8)
                                 ELSE seq > @after::integer END), false)
   AND (NOT @opening::boolean OR count(*) = 0)
   AND EXISTS (SELECT FROM live)
RETURNING seq;

-- A later turn only starts once this one is over, so one in flight with a later turn was cut off.
-- name: FinishTurn :execrows
UPDATE turns SET status = @status, agent_text = @agent_text, finished_at = now()
WHERE turns.workspace_id = @workspace_id AND turns.id = @id AND turns.status = 'in_flight'
  AND NOT EXISTS (SELECT FROM turns later WHERE later.workspace_id = turns.workspace_id AND later.session_id = turns.session_id AND later.seq > turns.seq);

-- A cut turn keeps its whole reply; heard_chars counts the characters of it the user heard, which
-- must be how the saved reply starts. Only the session's last turn can be cut, and only once
-- answered: a reply the model didn't finish stays lost. The session's next turn waits for the cut
-- before building its prompt; one that didn't, on another instance say, holds the whole reply.
-- name: CutTurn :execrows
UPDATE turns SET status = 'cut', heard_chars = char_length(@heard::text)
WHERE turns.workspace_id = @workspace_id AND turns.id = @id AND turns.status = 'answered'
  AND starts_with(turns.agent_text, @heard::text)
  AND NOT EXISTS (SELECT FROM turns later WHERE later.workspace_id = turns.workspace_id AND later.session_id = turns.session_id AND later.seq > turns.seq);

-- A turn the user talked on after, before any of its reply reached them, is removed while it is
-- still in flight, as if never taken: no other turn can have started meanwhile, or finished.
-- name: RemoveTurn :execrows
DELETE FROM turns WHERE workspace_id = @workspace_id AND id = @id AND status = 'in_flight';

-- A turn in flight past the deadline was cut off, by a crash say: saved lost, as StartTurn does.
-- name: CutOffTurns :exec
UPDATE turns SET status = 'lost', finished_at = now()
WHERE workspace_id = @workspace_id AND session_id = @session_id AND status = 'in_flight'
  AND created_at <= now() - make_interval(secs => @deadline_seconds::float8);

-- name: TurnInFlight :one
SELECT EXISTS (
    SELECT FROM turns
    WHERE workspace_id = @workspace_id AND session_id = @session_id AND status = 'in_flight'
      AND created_at > now() - make_interval(secs => @deadline_seconds::float8)
) AS in_flight;

-- name: FinishedTurns :many
SELECT seq, request_id, user_text, screen_input, agent_text, status, heard_chars, opening FROM turns
WHERE workspace_id = @workspace_id AND session_id = @session_id AND status <> 'in_flight'
ORDER BY seq;

-- A turn's stages, copied in one round trip; the foreign key checks each row against the turn's
-- workspace and user.
-- name: AddStages :copyfrom
INSERT INTO turn_stages (turn_id, workspace_id, user_id, stage, implementation, started_at, duration_ms, details)
VALUES (@turn_id, @workspace_id, @user_id, @stage, @implementation, @started_at, @duration_ms, @details);

-- name: Webhook :one
SELECT url, secret, (stopped_at IS NOT NULL)::boolean AS stopped FROM webhooks WHERE workspace_id = @workspace_id;

-- Setting the URL again keeps the secret and starts deliveries again: the secret given is kept only by
-- a workspace with none, and the one kept is returned.
-- name: SetWebhookURL :one
INSERT INTO webhooks (workspace_id, url, secret) VALUES (@workspace_id, @url, @secret)
ON CONFLICT (workspace_id) DO UPDATE SET url = EXCLUDED.url, stopped_at = NULL
RETURNING secret;

-- name: RotateWebhookSecret :execrows
UPDATE webhooks SET secret = @secret WHERE workspace_id = @workspace_id;

-- Deliveries stop only while the URL is still the one whose server answered 410 Gone; stopping again
-- keeps the time they first stopped.
-- name: StopWebhook :exec
UPDATE webhooks SET stopped_at = coalesce(stopped_at, now()) WHERE workspace_id = @workspace_id AND url = @url;
