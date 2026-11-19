-- Product keys: the secret a product's server sends to prove which workspace it acts for.

-- +goose Up
CREATE TABLE product_keys (
    id           uuid PRIMARY KEY CHECK (uuid_extract_version(id) = 7),
    workspace_id uuid NOT NULL REFERENCES workspaces (id),
    -- The key's SHA-256. The key itself is shown once, when made, and never kept, so a copy of this
    -- table can't be used to act as a product.
    hash         bytea NOT NULL UNIQUE CHECK (octet_length(hash) = 32),
    -- The key's first characters, so keys can be told apart without the key.
    display      text NOT NULL CHECK (display <> ''),
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- Set once, when the key stops working.
    revoked_at   timestamptz
);

-- +goose Down
DROP TABLE product_keys;
