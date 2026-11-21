-- What each product key lets its holder do, fixed when the key is made.

-- +goose Up
-- Every key made before could run sessions and nothing else, so each keeps exactly what it could do.
ALTER TABLE product_keys ADD COLUMN permissions text[] NOT NULL DEFAULT '{run_sessions}'
    CHECK (cardinality(permissions) > 0 AND permissions <@ '{run_sessions,publish_agents}');

-- +goose Down
-- Without the column every working key could run sessions again, so a key made without run_sessions
-- is revoked first: a rollback must never let a key do more than it was made for.
UPDATE product_keys SET revoked_at = now() WHERE revoked_at IS NULL AND NOT 'run_sessions' = ANY (permissions);
ALTER TABLE product_keys DROP COLUMN permissions;
