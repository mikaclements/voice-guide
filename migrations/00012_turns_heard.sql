-- How much of a cut turn's reply the user heard.

-- +goose Up
-- Characters of agent_text the user heard before cutting in, on a cut turn only: the whole reply
-- stays, so what the agent didn't get to say is kept, and later prompts hold only the part heard.
ALTER TABLE turns
    ADD COLUMN heard_chars integer CHECK (heard_chars >= 0 AND heard_chars <= char_length(agent_text)),
    ADD CONSTRAINT turns_heard_only_when_cut CHECK ((status = 'cut') = (heard_chars IS NOT NULL));

-- +goose Down
ALTER TABLE turns DROP CONSTRAINT turns_heard_only_when_cut, DROP COLUMN heard_chars;
