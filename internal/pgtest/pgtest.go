// Package pgtest gives each test its own PostgreSQL database with every
// migration applied, so store adapters are tested against the real thing and
// tests never see each other's rows.
package pgtest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/migrations"
)

// AdminURL names the variable holding a connection to a PostgreSQL server the
// tests may create databases on; scripts/check.sh sets it.
const AdminURL = "TEST_DATABASE_URL"

// Built once per test binary and shared: package state is where a helper used by many tests keeps it.
var (
	templateOnce sync.Once
	templateName string
	templateErr  error
)

// New returns a pool on a fresh, migrated database, dropped when the test
// ends. Without a server it skips the test, except in CI, where it fails, so
// these tests never go unrun there.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin := os.Getenv(AdminURL)
	if admin == "" {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s is not set; CI must run the database tests", AdminURL)
		}
		t.Skipf("%s is not set; scripts/check.sh starts a database and runs these tests too", AdminURL)
	}
	ctx := context.Background()
	templateOnce.Do(func() { templateName, templateErr = migratedTemplate(ctx, admin) })
	if templateErr != nil {
		t.Fatalf("prepare the test database: %v", templateErr)
	}

	name := "test_" + randomHex()
	exec(t, admin, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s STRATEGY FILE_COPY", name, templateName))
	t.Cleanup(func() { exec(t, admin, fmt.Sprintf("DROP DATABASE %s WITH (FORCE)", name)) })

	pool, err := pgxpool.New(ctx, withDatabase(admin, name))
	if err != nil {
		t.Fatalf("connect to %s: %v", name, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Exec runs one statement on the pool, failing the test if it errs.
func Exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// AddWorkspaces inserts the two workspaces the store tests share, so each test can show one
// workspace never reaching the other's rows.
func AddWorkspaces(t *testing.T, pool *pgxpool.Pool, first, second uuid.UUID) {
	t.Helper()
	Exec(t, pool, "INSERT INTO workspaces (id, name) VALUES ($1, 'Harbour'), ($2, 'Ferry port')", first, second)
}

// migratedTemplate returns a database with every migration applied, named for
// the migrations' content, so test runs reuse it until a migration changes. A
// lock keeps test packages running at once from building it twice. Templates
// are never dropped here, since another checkout's tests may be using an older
// one on the same server; `docker compose down -v` clears them all.
func migratedTemplate(ctx context.Context, admin string) (string, error) {
	name, err := templateNameFor()
	if err != nil {
		return "", err
	}
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		return "", fmt.Errorf("connect to %s: %w", AdminURL, err)
	}
	// Closing the connection releases the lock taken below; a failed close changes nothing for the caller.
	defer func() { _ = conn.Close(ctx) }()
	// Another package's test run may be building the template; that takes seconds, so 2 minutes
	// means it is stuck: a round guess.
	wait, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := conn.Exec(wait, "SELECT pg_advisory_lock(hashtext('pgtest'))"); err != nil {
		return "", fmt.Errorf("wait for another test run building the template: %w", err)
	}

	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return "", fmt.Errorf("look for the template: %w", err)
	}
	if exists {
		return name, nil
	}
	// Built under another name and renamed when complete, so a run that stops
	// halfway never leaves a template that looks finished.
	building := name + "_building"
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+building); err != nil {
		return "", fmt.Errorf("drop a half-built template: %w", err)
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+building); err != nil {
		return "", fmt.Errorf("create the template: %w", err)
	}
	if _, err := migrations.Up(ctx, withDatabase(admin, building)); err != nil {
		return "", fmt.Errorf("migrate: %w", err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("ALTER DATABASE %s RENAME TO %s", building, name)); err != nil {
		return "", fmt.Errorf("finish the template: %w", err)
	}
	return name, nil
}

func templateNameFor() (string, error) {
	h := sha256.New()
	err := fs.WalkDir(migrations.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(migrations.Files, path)
		h.Write([]byte(path))
		h.Write(b)
		return err
	})
	return "pgtest_template_" + hex.EncodeToString(h.Sum(nil))[:16], err
}

func exec(t *testing.T, admin, sql string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect to %s: %v", AdminURL, err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func withDatabase(admin, name string) string {
	u, err := url.Parse(admin)
	if err != nil {
		panic(fmt.Sprintf("%s is not a URL: %v", AdminURL, err))
	}
	u.Path = "/" + name
	return u.String()
}

func randomHex() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand never fails on supported platforms
	return hex.EncodeToString(b)
}
