-- Turn stages: the stages speech times for a spoken reply, saved with the model's.

-- +goose Up
-- A new stage is a migration, so every stage a turn records is one this list knows.
-- started_at + duration_ms is when the stage was reached. Speech's five stages all
-- start when the user started waiting, so their durations overlap: don't add them up.
ALTER TABLE turn_stages DROP CONSTRAINT turn_stages_stage_check;
-- Added without checking the rows already there, then checked on its own: adding it
-- checked would hold the table's lock while every row is read, and copies of voice
-- still serving during a deploy would wait on it.
ALTER TABLE turn_stages ADD CONSTRAINT turn_stages_stage_check CHECK (stage IN (
    'prompt', 'model_first_text', 'model_reply', 'save',
    'final_text', 'end_of_turn_check', 'taken', 'first_phrase_ready', 'first_audio_sent')) NOT VALID;
ALTER TABLE turn_stages VALIDATE CONSTRAINT turn_stages_stage_check;

-- +goose Down
DELETE FROM turn_stages WHERE stage IN ('final_text', 'end_of_turn_check', 'taken', 'first_phrase_ready', 'first_audio_sent');
ALTER TABLE turn_stages DROP CONSTRAINT turn_stages_stage_check;
ALTER TABLE turn_stages ADD CONSTRAINT turn_stages_stage_check CHECK (stage IN ('prompt', 'model_first_text', 'model_reply', 'save')) NOT VALID;
ALTER TABLE turn_stages VALIDATE CONSTRAINT turn_stages_stage_check;
