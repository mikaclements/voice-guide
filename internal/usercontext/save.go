package usercontext

import (
	"context"
	"fmt"
	"strings"
	"time"
	"uuid"
)

// SummaryRequest saves what a session learned about its user: the summary
// the session's agent wrote of it.
type SummaryRequest struct {
	Workspace uuid.UUID
	User      uuid.UUID
	Agent     uuid.UUID
	Session   uuid.UUID
	// SessionEnded is when the session ended, which orders its summary.
	SessionEnded time.Time
	Content      string
}

// SaveSummary saves the session's summary, which only the agent that wrote it
// loads, and returns it as saved. A session has one summary: saving again,
// as a job run a second time does, returns the first and saves nothing.
func (s *Service) SaveSummary(ctx context.Context, r SummaryRequest) (Item, error) {
	switch {
	case r.Workspace == uuid.Nil() || r.User == uuid.Nil() || r.Agent == uuid.Nil() || r.Session == uuid.Nil() || r.SessionEnded.IsZero():
		return Item{}, fmt.Errorf("%w: a summary needs its workspace, user, agent, session and when the session ended", ErrBadRequest)
	case strings.TrimSpace(r.Content) == "":
		return Item{}, fmt.Errorf("%w: a summary needs content", ErrBadRequest)
	case strings.ContainsRune(r.Content, 0):
		return Item{}, fmt.Errorf("%w: a NUL character can't be stored", ErrBadRequest)
	}
	saved, _, err := s.items.AddSummary(ctx, Item{
		ID: uuid.NewV7(), Workspace: r.Workspace, User: r.User, Kind: KindSessionSummary,
		Content: r.Content, Agent: r.Agent, Session: r.Session, SessionEnded: r.SessionEnded,
	})
	if err != nil {
		return Item{}, fmt.Errorf("save the session's summary: %w", err)
	}
	return saved, nil
}

// SessionSummary returns the summary of the user's session as it stands, the
// one saved or a correction that replaced it, if one was saved: for the
// session's product, which hears it once the session has ended.
func (s *Service) SessionSummary(ctx context.Context, workspace, user, session uuid.UUID) (Item, bool, error) {
	it, found, err := s.items.SessionSummary(ctx, workspace, user, session)
	if err != nil {
		return Item{}, false, fmt.Errorf("read the session's summary: %w", err)
	}
	return it, found, nil
}
