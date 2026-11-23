-- The earlier sessions each session carries: those of its user with its agent still live, or whose
-- summary was still coming, when it started.

-- +goose Up
-- By ID, in the order they started; every turn of the session holds their turns in its prompt, read
-- back from them, so any instance builds the same one.
ALTER TABLE sessions ADD COLUMN carried_session_ids uuid[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE sessions DROP COLUMN carried_session_ids;
