// Package migrations holds the database's migrations, numbered across modules,
// each file changing one module's schema. They are embedded so the migrate
// command and the tests apply exactly the files in the build.
package migrations

import (
	"context"
	"embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib" // database/sql over pgx, which goose uses
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// Files holds every migration, in the form goose reads.
//
//go:embed *.sql
var Files embed.FS

// lockTimeout bounds a migration's wait for a table lock, so a change stuck behind running
// queries fails the deploy step instead of holding every later query in line behind it. Queries
// here take milliseconds, so 10s means something is stuck: a round guess.
const lockTimeout = "10s"

// Up applies every migration the database hasn't run yet, in order. A lock in
// the database makes a second run wait for the first instead of racing it.
func Up(ctx context.Context, databaseURL string) (applied []*goose.MigrationResult, err error) {
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("database URL: %w", err)
	}
	cfg.RuntimeParams["lock_timeout"] = lockTimeout
	db := stdlib.OpenDB(*cfg)
	defer func() { err = errors.Join(err, db.Close()) }()
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("migration lock: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, Files, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	applied, err = p.Up(ctx)
	if err != nil {
		return applied, fmt.Errorf("migrate: %w", err)
	}
	return applied, nil
}
