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
	err := db.New(st.pool).AddProductKey(ctx, db.AddProductKeyParams{ID: k.ID, WorkspaceID: k.Workspace, Hash: k.Hash, Display: k.Display})
	if err != nil {
		return fmt.Errorf("add the product key: %w", err)
	}
	return nil
}

// Workspace returns the workspace of the working key with the hash.
func (st *Keys) Workspace(ctx context.Context, hash []byte) (uuid.UUID, bool, error) {
	ws, err := db.New(st.pool).ProductKeyWorkspace(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil(), false, nil
	}
	if err != nil {
		return uuid.Nil(), false, fmt.Errorf("find the product key: %w", err)
	}
	return ws, true, nil
}

// List returns the workspace's keys, newest first.
func (st *Keys) List(ctx context.Context, workspace uuid.UUID) ([]productkey.Record, error) {
	rows, err := db.New(st.pool).ProductKeys(ctx, workspace)
	if err != nil {
		return nil, fmt.Errorf("list the product keys: %w", err)
	}
	keys := make([]productkey.Record, 0, len(rows))
	for _, r := range rows {
		k := productkey.Record{ID: r.ID, Workspace: r.WorkspaceID, Hash: r.Hash, Display: r.Display, CreatedAt: r.CreatedAt}
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
