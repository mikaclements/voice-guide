-- What each product key lets its holder do, fixed when the key is made.

-- +goose Up
-- Every key made before could run sessions and nothing else, so each keeps exactly what it could do.
ALTER TABLE product_keys ADD COLUMN permissions text[] NOT NULL DEFAULT '{run_sessions}'
    CHECK (cardinality(permissions) > 0 AND permissions <@ '{run_sessions,publish_agents}');

-- +goose Down
ALTER TABLE product_keys DROP COLUMN permissions;
