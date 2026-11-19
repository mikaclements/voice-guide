-- Workspaces own agents; voice's records live in the default schema, and every module's point here.

-- +goose Up
CREATE TABLE workspaces (
    -- Every ID is a UUIDv7, so no central counter hands them out.
    id         uuid PRIMARY KEY CHECK (uuid_extract_version(id) = 7),
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE workspaces;
