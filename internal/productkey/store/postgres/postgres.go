// Package postgres keeps product keys in PostgreSQL. The queries are in
// queries.sql; sqlc generates the db package from them.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/productkey"
	"github.com/mikaclements/voice-guide/internal/productkey/store/postgres/db"
)

// Keys keeps product keys in PostgreSQL.
type Keys struct {
	pool *pgxpool.Pool
}

// New returns the store on pool.
func New(pool *pgxpool.Pool) *Keys { return &Keys{pool: pool} }

// Add saves k.
func (st *Keys) Add(ctx context.Context, k productkey.Record) error {
	err := db.New(st.pool).AddProductKey(ctx, db.AddProductKeyParams{
		ID: k.ID, WorkspaceID: k.Workspace, Hash: k.Hash, Display: k.Display, Permissions: k.Permissions.Names(),
	})
	if err != nil {
		return fmt.Errorf("add the product key: %w", err)
	}
	return nil
}

// Find returns the working key with the hash: its workspace and permissions.
func (st *Keys) Find(ctx context.Context, hash []byte) (productkey.Record, bool, error) {
	r, err := db.New(st.pool).WorkingProductKey(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return productkey.Record{}, false, nil
	}
	if err != nil {
		return productkey.Record{}, false, fmt.Errorf("find the product key: %w", err)
	}
	permissions, err := access.ParsePermissions(r.Permissions)
	if err != nil {
		return productkey.Record{}, false, fmt.Errorf("read the product key's permissions: %w", err)
	}
	return productkey.Record{Workspace: r.WorkspaceID, Hash: hash, Permissions: permissions}, true, nil
}

// List returns the workspace's keys, newest first.
func (st *Keys) List(ctx context.Context, workspace uuid.UUID) ([]productkey.Record, error) {
	rows, err := db.New(st.pool).ProductKeys(ctx, workspace)
	if err != nil {
		return nil, fmt.Errorf("list the product keys: %w", err)
	}
	keys := make([]productkey.Record, 0, len(rows))
	for _, r := range rows {
		permissions, err := access.ParsePermissions(r.Permissions)
		if err != nil {
			return nil, fmt.Errorf("read the permissions of product key %s: %w", r.ID, err)
		}
		k := productkey.Record{ID: r.ID, Workspace: r.WorkspaceID, Hash: r.Hash, Display: r.Display, Permissions: permissions, CreatedAt: r.CreatedAt}
		if r.RevokedAt.Valid {
			k.RevokedAt = r.RevokedAt.Time
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// Working counts the workspace's keys that work.
func (st *Keys) Working(ctx context.Context, workspace uuid.UUID) (int, error) {
	n, err := db.New(st.pool).WorkingProductKeys(ctx, workspace)
	if err != nil {
		return 0, fmt.Errorf("count the working product keys: %w", err)
	}
	return int(n), nil
}

// Revoke stops the workspace's key with id working, if it hasn't already.
func (st *Keys) Revoke(ctx context.Context, workspace, id uuid.UUID) (bool, error) {
	n, err := db.New(st.pool).RevokeProductKey(ctx, db.RevokeProductKeyParams{WorkspaceID: workspace, ID: id})
	if err != nil {
		return false, fmt.Errorf("revoke the product key: %w", err)
	}
	return n == 1, nil
}
