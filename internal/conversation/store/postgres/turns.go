package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/conversation/store/postgres/db"
)

// Turns keeps the turns of sessions in PostgreSQL.
type Turns struct {
	pool *pgxpool.Pool
}

// NewTurns returns the store of turns on pool.
func NewTurns(pool *pgxpool.Pool) *Turns {
	return &Turns{pool: pool}
}

// Start saves t in flight as its session's next turn. The checks, the number
// and marking a turn cut off as lost are one statement, so of two at once,
// the unique key on the number refuses the second, an opening among them.
func (st *Turns) Start(ctx context.Context, t conversation.Turn, after int, deadline time.Duration) (int, error) {
	screenInput, err := encodeBlocks(t.ScreenInput)
	if err != nil {
		return 0, fmt.Errorf("encode the screen input: %w", err)
	}
	q := db.New(st.pool)
	seq, err := q.StartTurn(ctx, db.StartTurnParams{
		ID: t.ID, WorkspaceID: t.Workspace, UserID: t.User, SessionID: t.Session, RequestID: t.RequestID,
		UserText: t.Words, ScreenInput: screenInput, DeadlineSeconds: deadline.Seconds(), After: int32(after), Opening: t.Opening,
	})
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Refused: the session ended, another turn holds it, or, for an opening, it has a turn.
		// Read again to say which; a session ends only once, so one ended now had ended when
		// the turn was refused.
		ended, err := q.SessionEnded(ctx, db.SessionEndedParams{WorkspaceID: t.Workspace, ID: t.Session})
		switch {
		case err != nil:
			return 0, fmt.Errorf("read why the turn was refused: %w", err)
		case !ended && !t.Opening:
			return 0, conversation.ErrTurnInFlight
		}
		// A retry of a turn the session took is answered as a repeat would be.
		status, err := q.TurnForRequest(ctx, db.TurnForRequestParams{WorkspaceID: t.Workspace, SessionID: t.Session, RequestID: t.RequestID})
		switch {
		case errors.Is(err, pgx.ErrNoRows) && !ended:
			return 0, conversation.ErrOpeningRefused
		case errors.Is(err, pgx.ErrNoRows):
			return 0, conversation.ErrSessionEnded
		case err != nil:
			return 0, fmt.Errorf("read why the turn was refused: %w", err)
		case conversation.TurnStatus(status) == conversation.TurnInFlight:
			return 0, conversation.ErrTurnInFlight
		}
		return 0, conversation.ErrTurnRepeated
	case errors.As(err, &pgErr) && pgErr.ConstraintName == "turns_session_id_seq_key" && t.Opening:
		return 0, conversation.ErrOpeningRefused
	case errors.As(err, &pgErr) && pgErr.ConstraintName == "turns_session_id_seq_key":
		return 0, conversation.ErrTurnInFlight
	case errors.As(err, &pgErr) && pgErr.ConstraintName == "turns_session_id_request_id_key":
		return 0, conversation.ErrTurnRepeated
	case err != nil:
		return 0, fmt.Errorf("start the turn: %w", err)
	}
	return int(seq), nil
}

// ForRequest returns the status of the session's turn with requestID, if any.
func (st *Turns) ForRequest(ctx context.Context, workspace, session, requestID uuid.UUID) (conversation.TurnStatus, bool, error) {
	status, err := db.New(st.pool).TurnForRequest(ctx, db.TurnForRequestParams{WorkspaceID: workspace, SessionID: session, RequestID: requestID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read the request's turn: %w", err)
	}
	return conversation.TurnStatus(status), true, nil
}

// InFlight says whether the session has a turn in flight younger than deadline.
func (st *Turns) InFlight(ctx context.Context, workspace, session uuid.UUID, deadline time.Duration) (bool, error) {
	inFlight, err := db.New(st.pool).TurnInFlight(ctx, db.TurnInFlightParams{WorkspaceID: workspace, SessionID: session, DeadlineSeconds: deadline.Seconds()})
	if err != nil {
		return false, fmt.Errorf("look for a turn in flight: %w", err)
	}
	return inFlight, nil
}

// CutOff saves lost every turn of the session in flight past deadline.
func (st *Turns) CutOff(ctx context.Context, workspace, session uuid.UUID, deadline time.Duration) error {
	if err := db.New(st.pool).CutOffTurns(ctx, db.CutOffTurnsParams{WorkspaceID: workspace, SessionID: session, DeadlineSeconds: deadline.Seconds()}); err != nil {
		return fmt.Errorf("save lost the turns cut off: %w", err)
	}
	return nil
}

// Finish saves how the turn in flight ended.
func (st *Turns) Finish(ctx context.Context, t conversation.Turn) error {
	n, err := db.New(st.pool).FinishTurn(ctx, db.FinishTurnParams{Status: string(t.Status), AgentText: t.Reply, WorkspaceID: t.Workspace, ID: t.ID})
	if err != nil {
		return fmt.Errorf("finish the turn: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("finish the turn: no turn %s in flight in the workspace", t.ID)
	}
	return nil
}

// Cut saves that the user heard only the first t.Heard bytes of an answered
// turn's reply. The column counts characters, as PostgreSQL's text functions
// do, so the part heard reads the same from SQL; the database counts them
// from the part heard, after checking the saved reply starts with it.
func (st *Turns) Cut(ctx context.Context, t conversation.Turn) error {
	n, err := db.New(st.pool).CutTurn(ctx, db.CutTurnParams{Heard: t.HeardReply(), WorkspaceID: t.Workspace, ID: t.ID})
	if err != nil {
		return fmt.Errorf("cut the turn: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("cut the turn: no answered turn %s in the workspace, starting with the part heard, without a later turn", t.ID)
	}
	return nil
}

// Remove deletes the turn t while it is in flight.
func (st *Turns) Remove(ctx context.Context, t conversation.Turn) error {
	n, err := db.New(st.pool).RemoveTurn(ctx, db.RemoveTurnParams{WorkspaceID: t.Workspace, ID: t.ID})
	if err != nil {
		return fmt.Errorf("remove the turn: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("remove the turn: no turn %s in flight in the workspace", t.ID)
	}
	return nil
}

// Finished returns the session's turns no longer in flight, oldest first.
func (st *Turns) Finished(ctx context.Context, workspace, session uuid.UUID) ([]conversation.Turn, error) {
	rows, err := db.New(st.pool).FinishedTurns(ctx, db.FinishedTurnsParams{WorkspaceID: workspace, SessionID: session})
	if err != nil {
		return nil, fmt.Errorf("read the session's turns: %w", err)
	}
	turns := make([]conversation.Turn, 0, len(rows))
	for _, r := range rows {
		screenInput, err := decodeBlocks[conversation.ScreenInputBlock](r.ScreenInput)
		if err != nil {
			return nil, fmt.Errorf("decode the screen input: %w", err)
		}
		turns = append(turns, conversation.Turn{
			Workspace: workspace, Session: session, Seq: int(r.Seq), RequestID: r.RequestID,
			ScreenInput: screenInput, Words: r.UserText, Reply: r.AgentText, Status: conversation.TurnStatus(r.Status),
			Heard: bytesOf(r.AgentText, int(r.HeardChars.Int32)), Opening: r.Opening,
		})
	}
	return turns, nil
}

// bytesOf returns how many bytes the first chars characters of s take.
func bytesOf(s string, chars int) int {
	n := 0
	for i := range s {
		if n == chars {
			return i
		}
		n++
	}
	return len(s)
}

// RecordStages saves the stage times of a turn already saved, in one copy.
func (st *Turns) RecordStages(ctx context.Context, t conversation.Turn) error {
	rows := make([]db.AddStagesParams, 0, len(t.Stages))
	for _, s := range t.Stages {
		details, err := json.Marshal(detailsOf(s))
		if err != nil {
			return fmt.Errorf("encode the %s stage's details: %w", s.Name, err)
		}
		rows = append(rows, db.AddStagesParams{
			TurnID: t.ID, WorkspaceID: t.Workspace, UserID: t.User,
			Stage: s.Name, Implementation: s.Implementation, StartedAt: s.Started,
			// A stage is seconds at most; the column holds whole milliseconds, enough for the
			// stages that matter, which take tens to thousands of them.
			DurationMs: int32(s.Took.Round(time.Millisecond) / time.Millisecond),
			Details:    details,
		})
	}
	if _, err := db.New(st.pool).AddStages(ctx, rows); err != nil {
		return fmt.Errorf("save the turn's stage times: %w", err)
	}
	return nil
}

// stageDetailsJSON is what a model reply's stage adds: the tokens used and the
// model service's own times, which leave out the trip between it and voice.
type stageDetailsJSON struct {
	Usage            *usageJSON `json:"usage,omitempty"`
	ModelFirstTextMS *int64     `json:"model_first_text_ms,omitempty"`
	ModelTotalMS     *int64     `json:"model_total_ms,omitempty"`
}

type usageJSON struct {
	Input       int `json:"input_tokens"`
	CachedInput int `json:"cached_input_tokens"`
	CacheWrite  int `json:"cache_write_tokens"`
	Output      int `json:"output_tokens"`
}

// detailsOf is a stage's details: the model service's report on the reply's
// stage, nothing on the others.
func detailsOf(s conversation.Stage) stageDetailsJSON {
	if s.Name != conversation.StageModelReply || s.Report == nil {
		return stageDetailsJSON{}
	}
	out := stageDetailsJSON{ModelTotalMS: millis(&s.Report.Total)}
	if u := s.Report.Usage; u != nil {
		out.Usage = &usageJSON{Input: u.Input, CachedInput: u.CachedInput, CacheWrite: u.CacheWrite, Output: u.Output}
	}
	out.ModelFirstTextMS = millis(s.Report.FirstText)
	return out
}

func millis(d *time.Duration) *int64 {
	if d == nil {
		return nil
	}
	ms := d.Milliseconds()
	return &ms
}
