-- Which turn, if any, is the agent's opening.

-- +goose Up
-- The opening is its session's first turn and carries no words: the user said nothing, and later
-- prompts rebuild its user message as the fixed cue the model was sent in their place, which a
-- first turn sent with screen input alone and no words couldn't be told apart from without this.
ALTER TABLE turns
    ADD COLUMN opening boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT turns_opening_first_without_words CHECK (NOT opening OR (seq = 1 AND user_text = ''));

-- +goose Down
ALTER TABLE turns DROP CONSTRAINT turns_opening_first_without_words, DROP COLUMN opening;
