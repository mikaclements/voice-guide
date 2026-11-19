// Package postgres keeps agent versions in PostgreSQL, in the agents module's
// own schema. The queries are in queries.sql; sqlc generates the db package
// from them.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/agents/store/postgres/db"
)

// Versions keeps agent versions in PostgreSQL.
type Versions struct {
	pool *pgxpool.Pool
}

// New returns the store on pool.
func New(pool *pgxpool.Pool) *Versions {
	return &Versions{pool: pool}
}

// Newest returns the newest version of each of the workspace's agents, by name.
func (s *Versions) Newest(ctx context.Context, workspace uuid.UUID) ([]agents.Version, error) {
	rows, err := db.New(s.pool).NewestVersions(ctx, workspace)
	if err != nil {
		return nil, fmt.Errorf("read newest agent versions: %w", err)
	}
	out := make([]agents.Version, len(rows))
	for i, r := range rows {
		out[i] = version(r.AgentID, r.Version, r.Name, r.Description, r.Reads, r.SessionContext, r.Persona, r.Summary, r.Opens, r.Model)
	}
	return out, nil
}

// Version returns one exact version, or agents.ErrNotFound.
func (s *Versions) Version(ctx context.Context, workspace, agent uuid.UUID, number int) (agents.Version, error) {
	if number < 1 || number > math.MaxInt32 {
		return agents.Version{}, agents.ErrNotFound
	}
	r, err := db.New(s.pool).Version(ctx, db.VersionParams{WorkspaceID: workspace, AgentID: agent, Version: int32(number)})
	if errors.Is(err, pgx.ErrNoRows) {
		return agents.Version{}, agents.ErrNotFound
	}
	if err != nil {
		return agents.Version{}, fmt.Errorf("read agent version: %w", err)
	}
	return version(r.AgentID, r.Version, r.Name, r.Description, r.Reads, r.SessionContext, r.Persona, r.Summary, r.Opens, r.Model), nil
}

// Save stores a as its next version unless the newest version has the same
// hash. The agent's row stays locked until the save commits, so two saves of
// one agent take turns choosing the next number.
func (s *Versions) Save(ctx context.Context, workspace uuid.UUID, a agents.Agent, hash string) (int, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("begin saving an agent version: %w", err)
	}
	// After a commit this does nothing; before one, the error already being returned matters more.
	defer func() { _ = tx.Rollback(ctx) }()

	q := db.New(tx)
	if err := q.AddAgent(ctx, db.AddAgentParams{ID: a.ID, WorkspaceID: workspace}); err != nil {
		if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return 0, false, fmt.Errorf("workspace %s doesn't exist; seed it before publishing", workspace)
		}
		return 0, false, fmt.Errorf("add the agent: %w", err)
	}
	if _, err := q.LockAgent(ctx, db.LockAgentParams{ID: a.ID, WorkspaceID: workspace}); errors.Is(err, pgx.ErrNoRows) {
		return 0, false, agents.ErrAgentInAnotherWorkspace
	} else if err != nil {
		return 0, false, fmt.Errorf("lock the agent: %w", err)
	}

	newest, err := q.NewestVersionOf(ctx, db.NewestVersionOfParams{WorkspaceID: workspace, AgentID: a.ID})
	switch {
	case errors.Is(err, pgx.ErrNoRows): // the first version
	case err != nil:
		return 0, false, fmt.Errorf("read the agent's newest version: %w", err)
	case newest.ContentHash == hash:
		return int(newest.Version), false, nil
	}
	// The columns hold an empty list, never NULL, and pgx sends a nil slice as NULL.
	reads := append([]string{}, a.Reads...)
	sessionContext := append([]string{}, a.SessionContext...)
	next := newest.Version + 1
	if err := q.AddVersion(ctx, db.AddVersionParams{
		WorkspaceID: workspace, AgentID: a.ID, Version: next,
		Name: a.Name, Description: a.Description, Reads: reads, SessionContext: sessionContext, Persona: a.Persona, Summary: a.Summary, Opens: a.Opens, Model: a.Model, ContentHash: hash,
	}); err != nil {
		return 0, false, fmt.Errorf("save the agent version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("commit the agent version: %w", err)
	}
	return int(next), true, nil
}

func version(id uuid.UUID, number int32, name, description string, reads, sessionContext []string, persona, summary string, opens bool, model string) agents.Version {
	return agents.Version{
		Agent: agents.Agent{
			ID: id, Name: name, Description: description, Reads: reads, SessionContext: sessionContext, Persona: persona, Summary: summary, Opens: opens, Model: model,
		},
		Number: int(number),
	}
}
