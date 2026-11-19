package conversation

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
)

// PastDefaultLimit is how many sessions the API's list returns when the
// product names no limit: a screen's worth of a user's history. A round guess,
// to be set from what products ask for once they list sessions.
const PastDefaultLimit = 20

// PastMaxLimit is the most sessions a list returns: each ended one costs a
// small read of its summary, so a full page is 100 of them, one tenth of a
// second or so at a millisecond each. A round guess, to be measured against a
// user with a long history.
const PastMaxLimit = 100

// ErrPastLimit reports a list asked for fewer than one session or more than
// PastMaxLimit.
var ErrPastLimit = errors.New("bad limit")

// PastSession is a session as its product reads it later: the user by the
// product's own reference, and the summary, nil while the session is live or
// when none was written.
type PastSession struct {
	Session   Session
	Reference string
	Summary   *string
}

// PastSessions lists the sessions of the user the product names by its own
// reference, newest first, those started before the session before, or the
// newest for the nil UUID, at most limit, which is 1 to PastMaxLimit. A user the
// product never started a session for has none, which looks the same as a
// user with no sessions yet. Only a product's server may ask, and only for
// its own workspace.
func (s *Service) PastSessions(ctx context.Context, c access.Caller, reference string, before uuid.UUID, limit int) ([]PastSession, error) {
	if !c.Product {
		return nil, ErrNoSuchSession
	}
	if err := checkUserReference(reference); err != nil {
		return nil, err
	}
	if limit < 1 || limit > PastMaxLimit {
		return nil, fmt.Errorf("%w: from 1 to %d", ErrPastLimit, PastMaxLimit)
	}
	user, found, err := s.sessions.UserByReference(ctx, c.Workspace, reference)
	switch {
	case err != nil:
		return nil, fmt.Errorf("find the user: %w", err)
	case !found:
		return []PastSession{}, nil
	}
	sessions, err := s.sessions.ListForUser(ctx, c.Workspace, user, before, limit)
	if err != nil {
		return nil, fmt.Errorf("list the user's sessions: %w", err)
	}
	list := make([]PastSession, 0, len(sessions))
	for _, session := range sessions {
		past, err := s.past(ctx, session, reference)
		if err != nil {
			return nil, err
		}
		list = append(list, past)
	}
	return list, nil
}

// PastSession returns one session of the product's workspace with its
// turns, as the user heard them, oldest first: the whole session, however
// long, unlike a delivery. A session of another workspace is
// ErrNoSuchSession, as one that doesn't exist.
func (s *Service) PastSession(ctx context.Context, c access.Caller, id uuid.UUID) (PastSession, []Turn, error) {
	if !c.Product {
		return PastSession{}, nil, ErrNoSuchSession
	}
	session, found, err := s.sessions.InWorkspace(ctx, c.Workspace, id)
	switch {
	case err != nil:
		return PastSession{}, nil, fmt.Errorf("read the session: %w", err)
	case !found:
		return PastSession{}, nil, ErrNoSuchSession
	}
	reference, found, err := s.sessions.Reference(ctx, c.Workspace, session.User)
	switch {
	case err != nil:
		return PastSession{}, nil, fmt.Errorf("read the user's reference: %w", err)
	case !found:
		// Every session starts with its product naming the user, so this is a
		// fault to see in the log.
		return PastSession{}, nil, errors.New("the session's user has no reference")
	}
	past, err := s.past(ctx, session, reference)
	if err != nil {
		return PastSession{}, nil, err
	}
	turns, err := s.turns.Finished(ctx, c.Workspace, session.ID)
	if err != nil {
		return PastSession{}, nil, fmt.Errorf("read the session's turns: %w", err)
	}
	return past, turns, nil
}

// past adds an ended session's summary, and settles a summary status never
// recorded; a live one is still being talked through, and has neither.
func (s *Service) past(ctx context.Context, session Session, reference string) (PastSession, error) {
	past := PastSession{Session: session, Reference: reference}
	if !session.Ended() {
		return past, nil
	}
	summary, found, err := s.userContext.SessionSummary(ctx, session.Workspace, session.User, session.ID)
	if err != nil {
		return PastSession{}, err
	}
	if found {
		past.Summary = &summary.Content
	}
	// A session ended before its status was kept, or by an older build, has none recorded: it
	// settles from its summary, so null stays the mark of a live session.
	if session.SummaryStatus == "" {
		past.Session.SummaryStatus = settled(found)
	}
	return past, nil
}
