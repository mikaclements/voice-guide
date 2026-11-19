// Package postgres keeps conversation's sessions and their turns in PostgreSQL. The queries are in
// queries.sql; sqlc generates the db package from them.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/conversation/store/postgres/db"
)

// Sessions keeps sessions in PostgreSQL.
type Sessions struct {
	pool *pgxpool.Pool
}

// New returns the store on pool.
func New(pool *pgxpool.Pool) *Sessions {
	return &Sessions{pool: pool}
}

// Start saves s unless its user already started a session with requestID in
// its workspace, adding the user the first time a product's reference
// names them. A second start waits for the first to commit, then
// finds its session, or the user it added for the reference, so starts at
// once still make one of each.
func (st *Sessions) Start(ctx context.Context, s conversation.Session, requestID uuid.UUID, reference string) (conversation.Session, bool, error) {
	if s.AgentVersion < 1 || s.AgentVersion > math.MaxInt32 {
		return conversation.Session{}, false, fmt.Errorf("agent version %d out of range", s.AgentVersion)
	}
	tx, err := st.pool.Begin(ctx)
	if err != nil {
		return conversation.Session{}, false, fmt.Errorf("begin starting a session: %w", err)
	}
	// After a commit this does nothing; before one, the error already being returned matters more.
	defer func() { _ = tx.Rollback(ctx) }()

	sessionContext, err := encodeBlocks(s.SessionContext)
	if err != nil {
		return conversation.Session{}, false, fmt.Errorf("encode the session context: %w", err)
	}
	q := db.New(tx)
	if reference != "" {
		// The reference goes in first, its user's row checked only at the commit, so a start that
		// finds the reference added by another adds no user of its own.
		byReference := db.UserByReferenceParams{WorkspaceID: s.Workspace, Reference: reference}
		user, err := q.AddUserReference(ctx, db.AddUserReferenceParams{WorkspaceID: s.Workspace, Reference: reference, UserID: s.User})
		if errors.Is(err, pgx.ErrNoRows) {
			user, err = q.UserByReference(ctx, byReference)
		}
		if err != nil {
			return conversation.Session{}, false, fmt.Errorf("add the user reference: %w", err)
		}
		s.User = user
	}
	if err := q.AddUser(ctx, s.User); err != nil {
		return conversation.Session{}, false, fmt.Errorf("add user: %w", err)
	}
	added, err := q.AddSession(ctx, db.AddSessionParams{
		ID: s.ID, WorkspaceID: s.Workspace, UserID: s.User,
		AgentID: s.Agent, AgentVersion: int32(s.AgentVersion), RequestID: requestID,
		// pgx sends a nil slice as NULL; a session that loaded nothing holds an empty list.
		ContextItemIds: append([]uuid.UUID{}, s.ContextItems...),
		SessionContext: sessionContext,
	})
	isNew := err == nil
	got := db.SessionForRequestRow(added)
	if errors.Is(err, pgx.ErrNoRows) {
		// Each statement sees what was committed when it began, so this finds the session the
		// conflicting start committed.
		got, err = q.SessionForRequest(ctx, db.SessionForRequestParams{WorkspaceID: s.Workspace, UserID: s.User, RequestID: requestID})
	}
	if err != nil {
		return conversation.Session{}, false, fmt.Errorf("start session: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return conversation.Session{}, false, fmt.Errorf("commit the session: %w", err)
	}
	started, err := sessionFrom(got)
	return started, isNew, err
}

// UserByReference returns the user a product names by reference in the
// workspace, if a start has added them.
func (st *Sessions) UserByReference(ctx context.Context, workspace uuid.UUID, reference string) (uuid.UUID, bool, error) {
	user, err := db.New(st.pool).UserByReference(ctx, db.UserByReferenceParams{WorkspaceID: workspace, Reference: reference})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil(), false, nil
	}
	if err != nil {
		return uuid.Nil(), false, fmt.Errorf("find the user by reference: %w", err)
	}
	return user, true, nil
}

// Reference returns the reference the workspace's product names the user by,
// if a start from its server added one.
func (st *Sessions) Reference(ctx context.Context, workspace, user uuid.UUID) (string, bool, error) {
	reference, err := db.New(st.pool).UserReference(ctx, db.UserReferenceParams{WorkspaceID: workspace, UserID: user})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read the user's reference: %w", err)
	}
	return reference, true, nil
}

// ForRequest returns the session the user started with requestID in the workspace.
func (st *Sessions) ForRequest(ctx context.Context, workspace, user, requestID uuid.UUID) (conversation.Session, bool, error) {
	got, err := db.New(st.pool).SessionForRequest(ctx, db.SessionForRequestParams{WorkspaceID: workspace, UserID: user, RequestID: requestID})
	if errors.Is(err, pgx.ErrNoRows) {
		return conversation.Session{}, false, nil
	}
	if err != nil {
		return conversation.Session{}, false, fmt.Errorf("find the session for the request: %w", err)
	}
	s, err := sessionFrom(got)
	return s, err == nil, err
}

// ByID returns the user's session with id in the workspace.
func (st *Sessions) ByID(ctx context.Context, workspace, user, id uuid.UUID) (conversation.Session, bool, error) {
	got, err := db.New(st.pool).SessionByID(ctx, db.SessionByIDParams{WorkspaceID: workspace, UserID: user, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return conversation.Session{}, false, nil
	}
	if err != nil {
		return conversation.Session{}, false, fmt.Errorf("find the session: %w", err)
	}
	s, err := sessionFrom(db.SessionForRequestRow(got))
	return s, err == nil, err
}

// InWorkspace returns the session with id in the workspace, whichever user's it is.
func (st *Sessions) InWorkspace(ctx context.Context, workspace, id uuid.UUID) (conversation.Session, bool, error) {
	got, err := db.New(st.pool).SessionInWorkspace(ctx, db.SessionInWorkspaceParams{WorkspaceID: workspace, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return conversation.Session{}, false, nil
	}
	if err != nil {
		return conversation.Session{}, false, fmt.Errorf("find the session in the workspace: %w", err)
	}
	s, err := sessionFrom(db.SessionForRequestRow(got))
	return s, err == nil, err
}

// End marks the user's session with id ended, unless it already is,
// queuing its summary job in the same statement, then reads it back: a
// statement of its own sees an end another call committed meanwhile.
func (st *Sessions) End(ctx context.Context, workspace, user, id uuid.UUID) (conversation.Session, bool, error) {
	n, err := db.New(st.pool).EndSession(ctx, db.EndSessionParams{WorkspaceID: workspace, UserID: user, ID: id})
	if err != nil {
		return conversation.Session{}, false, fmt.Errorf("end the session: %w", err)
	}
	s, found, err := st.ByID(ctx, workspace, user, id)
	switch {
	case err != nil:
		return conversation.Session{}, false, err
	case !found:
		return conversation.Session{}, false, conversation.ErrNoSuchSession
	}
	return s, n == 1, nil
}

// Touch marks the user's live session active now.
func (st *Sessions) Touch(ctx context.Context, workspace, user, id uuid.UUID) (bool, error) {
	n, err := db.New(st.pool).TouchSession(ctx, db.TouchSessionParams{WorkspaceID: workspace, UserID: user, ID: id})
	if err != nil {
		return false, fmt.Errorf("mark the session active: %w", err)
	}
	return n == 1, nil
}

// EndIdle ends every live session not active for idle, in one statement.
func (st *Sessions) EndIdle(ctx context.Context, idle time.Duration) ([]uuid.UUID, error) {
	ended, err := db.New(st.pool).EndIdleSessions(ctx, idle.Seconds())
	if err != nil {
		return nil, fmt.Errorf("end idle sessions: %w", err)
	}
	return ended, nil
}

// block is a named text block, as session context and screen input are both sent.
type block interface {
	~struct{ Name, Content string }
}

// blockJSON is how a block is kept in a jsonb column.
type blockJSON struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// encodeBlocks returns the blocks as they are kept: a list, never null.
func encodeBlocks[B block](blocks []B) ([]byte, error) {
	out := make([]blockJSON, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, blockJSON(b))
	}
	return json.Marshal(out)
}

func decodeBlocks[B block](kept []byte) ([]B, error) {
	var stored []blockJSON
	if err := json.Unmarshal(kept, &stored); err != nil {
		return nil, err
	}
	blocks := make([]B, 0, len(stored))
	for _, b := range stored {
		blocks = append(blocks, B(b))
	}
	return blocks, nil
}

func sessionFrom(r db.SessionForRequestRow) (conversation.Session, error) {
	blocks, err := decodeBlocks[conversation.SessionContextBlock](r.SessionContext)
	if err != nil {
		return conversation.Session{}, fmt.Errorf("decode the session context: %w", err)
	}
	return conversation.Session{
		ID: r.ID, Workspace: r.WorkspaceID, User: r.UserID, Agent: r.AgentID, AgentVersion: int(r.AgentVersion),
		ContextItems: r.ContextItemIds, SessionContext: blocks, StartedAt: r.StartedAt, EndedAt: timeOrZero(r.EndedAt),
	}, nil
}

// timeOrZero returns t's time, or the zero time for NULL.
func timeOrZero(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}
