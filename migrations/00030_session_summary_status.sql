-- Whether each ended session's summary is still coming.

-- +goose Up
-- NULL while the session is live, and on sessions ended before the status was kept.
ALTER TABLE sessions ADD COLUMN summary_status text CHECK (summary_status IN ('pending', 'written', 'none'));

-- +goose Down
ALTER TABLE sessions DROP COLUMN summary_status;
