-- The structured part of each session's summary.

-- +goose Up
-- NULL until the summary job saves some, and for good when the agent has no summary schema or the data was dropped.
ALTER TABLE sessions ADD COLUMN summary_data jsonb;

-- +goose Down
ALTER TABLE sessions DROP COLUMN summary_data;
