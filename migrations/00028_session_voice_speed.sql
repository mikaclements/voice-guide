-- The voice and speed each session speaks in.

-- +goose Up
-- Empty and 0 are the server's own, as every session saved before it used.
ALTER TABLE sessions ADD COLUMN voice text NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN speed double precision NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sessions DROP COLUMN speed;
ALTER TABLE sessions DROP COLUMN voice;
