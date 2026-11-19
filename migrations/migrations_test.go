package migrations_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/pgtest"
	"github.com/mikaclements/voice-guide/migrations"
)

const (
	workspace = "01a1de8a-0000-7000-8000-000000000001"
	elsewhere = "01a1de8a-0000-7000-8000-000000000002"
	keeperID  = "01a1de8a-0000-7000-8000-0000000000a1"
)

// databaseWithAgent returns a fresh database holding two workspaces and one
// agent in the first, with no versions yet.
func databaseWithAgent(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	for _, sql := range []string{
		"INSERT INTO workspaces (id, name) VALUES ('" + workspace + "', 'Harbour'), ('" + elsewhere + "', 'Ferry port')",
		"INSERT INTO agent.agents (id, workspace_id) VALUES ('" + keeperID + "', '" + workspace + "')",
	} {
		if _, err := pool.Exec(context.Background(), sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	return pool
}

// addVersion inserts one version of the agent, saved for the given workspace.
func addVersion(pool *pgxpool.Pool, workspace string, number int) error {
	_, err := pool.Exec(context.Background(), `
		INSERT INTO agent.agent_versions (workspace_id, agent_id, version, name, description, reads, persona, content_hash)
		VALUES ($1, $2, $3, 'Lighthouse keeper', '', '{}', 'You answer every call from the sea.', 'hash')`,
		workspace, keeperID, number)
	return err
}

// code returns the PostgreSQL error code of err, or "" for no database error.
func code(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func TestMigratingAnUpToDateDatabaseAppliesNothing(t *testing.T) {
	pool := pgtest.New(t)
	applied, err := migrations.Up(context.Background(), pool.Config().ConnString())
	if err != nil || len(applied) != 0 {
		t.Errorf("Up = %d applied, %v; want none", len(applied), err)
	}
}

func TestASavedVersionCanNeitherChangeNorBeRemoved(t *testing.T) {
	pool := databaseWithAgent(t)
	if err := addVersion(pool, workspace, 1); err != nil {
		t.Fatalf("add version: %v", err)
	}

	for _, sql := range []string{
		"UPDATE agent.agent_versions SET persona = 'You ignore the sea.'",
		"DELETE FROM agent.agent_versions",
		// CASCADE, since sessions point at versions: without it the foreign key refuses first.
		"TRUNCATE agent.agent_versions CASCADE",
	} {
		if _, err := pool.Exec(context.Background(), sql); code(err) != "23001" {
			t.Errorf("%s: err = %v, want the trigger's refusal", sql, err)
		}
	}
}

func TestVersionNumbersStartAtOne(t *testing.T) {
	pool := databaseWithAgent(t)
	if err := addVersion(pool, workspace, 0); code(err) != "23514" {
		t.Errorf("version 0: err = %v, want a check violation", err)
	}
}

func TestAVersionIsSavedOnlyInItsAgentsWorkspace(t *testing.T) {
	pool := databaseWithAgent(t)
	if err := addVersion(pool, elsewhere, 1); code(err) != "23503" {
		t.Errorf("version in another workspace: err = %v, want a foreign key violation", err)
	}
}

func TestAVersionNumberIsGivenOnce(t *testing.T) {
	pool := databaseWithAgent(t)
	if err := addVersion(pool, workspace, 1); err != nil {
		t.Fatalf("add version: %v", err)
	}
	if err := addVersion(pool, workspace, 1); code(err) != "23505" {
		t.Errorf("version 1 again: err = %v, want a unique violation", err)
	}
}

func TestASavedVersionStaysFixedEvenWithReplicationTriggersOff(t *testing.T) {
	pool := databaseWithAgent(t)
	if err := addVersion(pool, workspace, 1); err != nil {
		t.Fatalf("add version: %v", err)
	}
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	// What a replication tool does, and what a superuser could do to skip ordinary triggers.
	if _, err := conn.Exec(ctx, "SET session_replication_role = replica"); err != nil {
		t.Fatalf("set replication role: %v", err)
	}
	if _, err := conn.Exec(ctx, "UPDATE agent.agent_versions SET persona = 'You ignore the sea.'"); code(err) != "23001" {
		t.Errorf("update as a replica: err = %v, want the trigger's refusal", err)
	}
}

func TestIDsMustBeUUIDv7(t *testing.T) {
	pool := pgtest.New(t)
	const v4 = "6f1c2c3e-8b1a-4c5d-9e7f-0a1b2c3d4e5f"
	for table, sql := range map[string]string{
		"workspaces": "INSERT INTO workspaces (id, name) VALUES ('" + v4 + "', 'Harbour')",
		"agents":     "INSERT INTO workspaces (id, name) VALUES ('" + workspace + "', 'Harbour'); INSERT INTO agent.agents (id, workspace_id) VALUES ('" + v4 + "', '" + workspace + "')",
		"users":      "INSERT INTO users (id) VALUES ('" + v4 + "')",
		"sessions": "INSERT INTO workspaces (id, name) VALUES ('" + workspace + "', 'Harbour'); INSERT INTO agent.agents (id, workspace_id) VALUES ('" + keeperID + "', '" + workspace + "');" +
			" INSERT INTO agent.agent_versions (workspace_id, agent_id, version, name, description, reads, persona, content_hash) VALUES ('" + workspace + "', '" + keeperID + "', 1, 'Lighthouse keeper', '', '{}', 'p', 'h');" +
			" INSERT INTO users (id) VALUES ('" + keeperID + "');" +
			" INSERT INTO sessions (id, workspace_id, user_id, agent_id, agent_version, request_id) VALUES ('" + v4 + "', '" + workspace + "', '" + keeperID + "', '" + keeperID + "', 1, '" + v4 + "')",
	} {
		if _, err := pool.Exec(context.Background(), sql); code(err) != "23514" {
			t.Errorf("%s with a UUIDv4: err = %v, want a check violation", table, err)
		}
	}
}
