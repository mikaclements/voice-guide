-- name: AddProductKey :exec
INSERT INTO product_keys (id, workspace_id, hash, display, permissions)
VALUES (@id, @workspace_id, @hash, @display, @permissions);

-- The key check finds the key by its hash, across workspaces: the key is what names the workspace.
-- name: WorkingProductKey :one
SELECT workspace_id, permissions FROM product_keys WHERE hash = @hash AND revoked_at IS NULL;

-- name: ProductKeys :many
SELECT id, workspace_id, hash, display, permissions, created_at, revoked_at FROM product_keys
WHERE workspace_id = @workspace_id ORDER BY id DESC;

-- name: WorkingProductKeys :one
SELECT count(*) FROM product_keys WHERE workspace_id = @workspace_id AND revoked_at IS NULL;

-- Revoking again keeps the time the key first stopped working.
-- name: RevokeProductKey :execrows
UPDATE product_keys SET revoked_at = coalesce(revoked_at, now())
WHERE workspace_id = @workspace_id AND id = @id;
