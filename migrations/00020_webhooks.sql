-- Webhooks: where each workspace's product hears that its sessions ended. Voice's records, in the
-- default schema.

-- +goose Up
CREATE TABLE webhooks (
    workspace_id uuid PRIMARY KEY REFERENCES workspaces (id),
    url          text NOT NULL CHECK (url ~ '^https?://' AND char_length(url) <= 2000),
    -- The secret deliveries are signed with: whsec_ and 32 random bytes in base64. Kept readable,
    -- unlike a product key's hash, since signing needs it; shown once, when made.
    secret       text NOT NULL CHECK (secret ~ '^whsec_[A-Za-z0-9+/]{43}=$'),
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- Set when the product's server answered 410 Gone: nothing is delivered until the URL is set again.
    stopped_at   timestamptz
);

-- +goose Down
DROP TABLE webhooks;
