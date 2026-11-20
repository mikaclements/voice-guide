-- The structured part of each session's summary.

-- +goose Up
-- json, not jsonb, so the data is kept as the model wrote it, its keys in order, and passed on so.
-- NULL until the summary job saves some, and for good when the agent has no summary schema or the data was dropped.
ALTER TABLE sessions ADD COLUMN summary_data json;

-- +goose Down
ALTER TABLE sessions DROP COLUMN summary_data;
