// Package postgres keeps agent versions in PostgreSQL, in the agents module's
// own schema. The queries are in queries.sql; sqlc generates the db package
// from them.
package postgres

import (
	"bytes"
	"context"
	"encoding/json"
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
		schema, err := decodeSchema(r.SummarySchema)
		if err != nil {
			return nil, fmt.Errorf("read the summary schema of agent %s version %d: %w", r.AgentID, r.Version, err)
		}
		out[i] = version(r.AgentID, r.Version, r.Name, r.Description, r.Reads, r.SessionContext, r.Persona, r.Summary, r.Opens, r.Model, r.Voice, r.Speed)
		out[i].SummaryModel, out[i].SummarySchema, out[i].SummaryDocuments = r.SummaryModel, schema, r.SummaryDocuments
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
	schema, err := decodeSchema(r.SummarySchema)
	if err != nil {
		return agents.Version{}, fmt.Errorf("read the agent version's summary schema: %w", err)
	}
	v := version(r.AgentID, r.Version, r.Name, r.Description, r.Reads, r.SessionContext, r.Persona, r.Summary, r.Opens, r.Model, r.Voice, r.Speed)
	v.SummaryModel, v.SummarySchema, v.SummaryDocuments = r.SummaryModel, schema, r.SummaryDocuments
	return v, nil
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
	schema := []byte(a.SummarySchema)
	if schema == nil {
		schema = noSchema
	}
	reads := append([]string{}, a.Reads...)
	sessionContext := append([]string{}, a.SessionContext...)
	next := newest.Version + 1
	if err := q.AddVersion(ctx, db.AddVersionParams{
		WorkspaceID: workspace, AgentID: a.ID, Version: next,
		Name: a.Name, Description: a.Description, Reads: reads, SessionContext: sessionContext, Persona: a.Persona, Summary: a.Summary, SummaryModel: a.SummaryModel, SummarySchema: schema, SummaryDocuments: append([]string{}, a.SummaryDocuments...), Opens: a.Opens, Model: a.Model, Voice: a.Voice, Speed: a.Speed, ContentHash: hash,
	}); err != nil {
		return 0, false, fmt.Errorf("save the agent version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("commit the agent version: %w", err)
	}
	return int(next), true, nil
}

func version(id uuid.UUID, number int32, name, description string, reads, sessionContext []string, persona, summary string, opens bool, model, voice string, speed float64) agents.Version {
	return agents.Version{
		Agent: agents.Agent{
			ID: id, Name: name, Description: description, Reads: reads, SessionContext: sessionContext, Persona: persona, Summary: summary, Opens: opens, Model: model, Voice: voice, Speed: speed,
		},
		Number: int(number),
	}
}

// The column holds {} for no schema, since it is never NULL; a schema has at
// least its type, so {} is never one.
var noSchema = []byte("{}")

// decodeSchema reads a kept schema back. PostgreSQL keeps jsonb in its own
// key order and spacing, so it is encoded again as the loader encodes it,
// keys sorted, giving back the bytes that were published.
func decodeSchema(kept []byte) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(kept))
	dec.UseNumber() // a number reads back exactly as written, never through a float
	var schema map[string]any
	if err := dec.Decode(&schema); err != nil {
		return nil, err
	}
	if len(schema) == 0 {
		return nil, nil
	}
	return json.Marshal(schema)
}
