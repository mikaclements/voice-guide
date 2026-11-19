// Package postgres keeps user context's items in PostgreSQL, in user context's
// own schema. The queries are in queries.sql; sqlc generates the db package
// from them.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/usercontext"
	"github.com/mikaclements/voice-guide/internal/usercontext/store/postgres/db"
)

// Items keeps context items in PostgreSQL.
type Items struct {
	q *db.Queries
}

// New returns the store on pool.
func New(pool *pgxpool.Pool) *Items {
	return &Items{q: db.New(pool)}
}

// Current returns the user's items of the given kinds in the workspace that no
// other item replaces, newest first, the session summaries only those agent wrote.
func (st *Items) Current(ctx context.Context, workspace, user uuid.UUID, kinds []string, agent uuid.UUID, limit int) ([]usercontext.Item, error) {
	rows, err := st.q.CurrentItems(ctx, db.CurrentItemsParams{WorkspaceID: workspace, UserID: user, Kinds: kinds, AgentID: agent, MaxRows: int32(min(limit, math.MaxInt32))})
	if err != nil {
		return nil, fmt.Errorf("read the user's current items: %w", err)
	}
	out := make([]usercontext.Item, 0, len(rows))
	for _, r := range rows {
		out = append(out, item(db.ItemsByIDRow(r)))
	}
	return out, nil
}

// AddSummary saves it unless its session already has a summary, and returns
// the session's summary and whether it is this one.
func (st *Items) AddSummary(ctx context.Context, it usercontext.Item) (usercontext.Item, bool, error) {
	r, err := st.q.AddSummary(ctx, db.AddSummaryParams{
		ID: it.ID, WorkspaceID: it.Workspace, UserID: it.User, Content: it.Content, AgentID: it.Agent, SourceSessionID: it.Session,
		SourceEndedAt: it.SessionEnded,
	})
	if err == nil {
		return item(db.ItemsByIDRow(r)), true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return usercontext.Item{}, false, fmt.Errorf("add the summary: %w", err)
	}
	// Each statement sees what was committed when it began, so this finds the summary that was.
	first, err := st.q.SummaryOfSession(ctx, db.SummaryOfSessionParams{WorkspaceID: it.Workspace, UserID: it.User, SourceSessionID: it.Session})
	if err != nil {
		return usercontext.Item{}, false, fmt.Errorf("read the session's summary: %w", err)
	}
	return item(db.ItemsByIDRow(first)), false, nil
}

// SessionSummary returns the user's summary of session in the workspace that
// no other item replaces, if it has one.
func (st *Items) SessionSummary(ctx context.Context, workspace, user, session uuid.UUID) (usercontext.Item, bool, error) {
	r, err := st.q.CurrentSummaryOfSession(ctx, db.CurrentSummaryOfSessionParams{WorkspaceID: workspace, UserID: user, SourceSessionID: session})
	if errors.Is(err, pgx.ErrNoRows) {
		return usercontext.Item{}, false, nil
	}
	if err != nil {
		return usercontext.Item{}, false, fmt.Errorf("read the session's summary: %w", err)
	}
	return item(db.ItemsByIDRow(r)), true, nil
}

// ByID returns those of ids that are the user's items in the workspace,
// replaced or not, in any order.
func (st *Items) ByID(ctx context.Context, workspace, user uuid.UUID, ids []uuid.UUID) ([]usercontext.Item, error) {
	rows, err := st.q.ItemsByID(ctx, db.ItemsByIDParams{WorkspaceID: workspace, UserID: user, Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("read the user's items by ID: %w", err)
	}
	out := make([]usercontext.Item, 0, len(rows))
	for _, r := range rows {
		out = append(out, item(r))
	}
	return out, nil
}

func item(r db.ItemsByIDRow) usercontext.Item {
	return usercontext.Item{
		ID: r.ID, Workspace: r.WorkspaceID, User: r.UserID, Kind: r.Kind, Content: r.Content,
		Replaces: replaced(r.Replaces), Agent: replaced(r.AgentID), Session: replaced(r.SourceSessionID), SessionEnded: endedAt(r.SourceEndedAt), CreatedAt: r.CreatedAt,
	}
}

// endedAt is a nullable column's time, or the zero time for NULL.
func endedAt(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}

// replaced is a nullable column's ID, or the nil UUID for NULL.
func replaced(id pgtype.UUID) uuid.UUID {
	if !id.Valid {
		return uuid.Nil()
	}
	return uuid.UUID(id.Bytes)
}
