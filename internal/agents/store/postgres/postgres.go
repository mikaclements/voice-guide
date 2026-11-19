// Package postgres keeps agent versions in PostgreSQL, in the agents module's
// own schema. The queries are in queries.sql; sqlc generates the db package
// from them.
package postgres

import (
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
		grading, err := decodeGrading(r.Grading)
		if err != nil {
			return nil, fmt.Errorf("read the grading of agent %s version %d: %w", r.AgentID, r.Version, err)
		}
		out[i] = version(r.AgentID, r.Version, r.Name, r.Description, r.Reads, r.SessionContext, r.Persona, r.Summary, r.Opens, r.Model, r.Voice, r.Speed)
		out[i].SummaryModel, out[i].Grading = r.SummaryModel, grading
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
	grading, err := decodeGrading(r.Grading)
	if err != nil {
		return agents.Version{}, fmt.Errorf("read the agent version's grading: %w", err)
	}
	v := version(r.AgentID, r.Version, r.Name, r.Description, r.Reads, r.SessionContext, r.Persona, r.Summary, r.Opens, r.Model, r.Voice, r.Speed)
	v.SummaryModel, v.Grading = r.SummaryModel, grading
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
	grading, err := json.Marshal(gradingJSONOf(a.Grading))
	if err != nil {
		return 0, false, fmt.Errorf("encode the agent's grading: %w", err)
	}
	reads := append([]string{}, a.Reads...)
	sessionContext := append([]string{}, a.SessionContext...)
	next := newest.Version + 1
	if err := q.AddVersion(ctx, db.AddVersionParams{
		WorkspaceID: workspace, AgentID: a.ID, Version: next,
		Name: a.Name, Description: a.Description, Reads: reads, SessionContext: sessionContext, Persona: a.Persona, Summary: a.Summary, SummaryModel: a.SummaryModel, Grading: grading, Opens: a.Opens, Model: a.Model, Voice: a.Voice, Speed: a.Speed, ContentHash: hash,
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

// gradingJSON is how a grading is kept in its column: an empty object when the
// agent grades nothing.
type gradingJSON struct {
	Lowest   int             `json:"lowest,omitempty"`
	Highest  int             `json:"highest,omitempty"`
	Levels   []levelJSON     `json:"levels,omitempty"`
	Criteria []criterionJSON `json:"criteria,omitempty"`
}

type levelJSON struct {
	Score int    `json:"score"`
	Means string `json:"means"`
}

type criterionJSON struct {
	Name     string `json:"name"`
	LooksFor string `json:"looks_for"`
}

func gradingJSONOf(g agents.Grading) gradingJSON {
	out := gradingJSON{Lowest: g.Lowest, Highest: g.Highest}
	for _, l := range g.Levels {
		out.Levels = append(out.Levels, levelJSON(l))
	}
	for _, c := range g.Criteria {
		out.Criteria = append(out.Criteria, criterionJSON(c))
	}
	return out
}

func decodeGrading(kept []byte) (agents.Grading, error) {
	var stored gradingJSON
	if err := json.Unmarshal(kept, &stored); err != nil {
		return agents.Grading{}, err
	}
	g := agents.Grading{Lowest: stored.Lowest, Highest: stored.Highest}
	for _, l := range stored.Levels {
		g.Levels = append(g.Levels, agents.Level(l))
	}
	for _, c := range stored.Criteria {
		g.Criteria = append(g.Criteria, agents.Criterion(c))
	}
	return g, nil
}
