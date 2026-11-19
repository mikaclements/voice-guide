package conversation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/usercontext"
)

// Session is one conversation between a user and one agent version. It keeps
// the version it started on, whatever is published later.
type Session struct {
	ID           uuid.UUID
	Workspace    uuid.UUID
	User         uuid.UUID
	Agent        uuid.UUID
	AgentVersion int
	// ContextItems are the IDs, in order, of what the session loaded about the
	// user; any instance rebuilds the same prompt from them on every turn.
	ContextItems []uuid.UUID
	// SessionContext is what the product sent, in order, kept for the whole
	// session.
	SessionContext []SessionContextBlock
	StartedAt      time.Time
	// EndedAt is when the session ended: zero while it is live.
	EndedAt time.Time
}

// Ended says whether the session has ended.
func (s Session) Ended() bool { return !s.EndedAt.IsZero() }

// StartRequest asks to start a session: the agent, the caller's request ID,
// the session context the agent's version expects from the product, and,
// from a product's server, its reference for the user: its own stable ID for
// them.
type StartRequest struct {
	Agent          uuid.UUID
	RequestID      uuid.UUID
	SessionContext []SessionContextBlock
	User           string
}

// maxUserReference bounds a user reference, in characters: room for any ID a
// product keeps for its users, while a reference stays an ID, not a document.
const maxUserReference = 200

// ErrUserReference reports a start whose user reference is missing or badly
// formed.
var ErrUserReference = errors.New("bad user reference")

// ErrUserUnavailable reports a start whose user reference couldn't be looked
// up. Nothing was saved, so the same request can be retried.
var ErrUserUnavailable = errors.New("the user couldn't be looked up")

// ErrRequestReused reports a request ID that already started a session for a
// different request, with another agent or different session context: a
// repeat must ask for the same thing as the first request.
var ErrRequestReused = errors.New("the request ID already started a session")

// ErrNoSuchAgent reports an agent the caller can't start a session with:
// unknown or another workspace's. Both look alike, so a caller learns nothing
// about agents outside its workspace.
var ErrNoSuchAgent = errors.New("no such agent")

// ErrSessionEnded reports a turn, or an audio connection, asked for in a
// session that has ended.
var ErrSessionEnded = errors.New("the session has ended")

// ErrUserContextUnavailable reports that a start needed user context and couldn't
// reach it. Nothing was saved, so the same request can be retried.
var ErrUserContextUnavailable = errors.New("user context is unavailable")

// Sessions keeps the sessions users start.
type Sessions interface {
	// ForRequest returns the user's session for requestID in the workspace, if any.
	ForRequest(ctx context.Context, workspace, user, requestID uuid.UUID) (Session, bool, error)
	// Start saves s unless its user already started a session with requestID
	// in its workspace, and returns the session that holds requestID and
	// whether it is new. A reference is the product's name for a user new to
	// voice-guide, s's user: it is added with the session, in the same write,
	// unless a start that committed meanwhile added it, whose user the session
	// is then for.
	Start(ctx context.Context, s Session, requestID uuid.UUID, reference string) (Session, bool, error)
	// ByID returns the user's session with id in the workspace, if any.
	ByID(ctx context.Context, workspace, user, id uuid.UUID) (Session, bool, error)
	// InWorkspace returns the session with id in the workspace, whichever
	// user's it is, if any: for a product's server, which names no user.
	InWorkspace(ctx context.Context, workspace, id uuid.UUID) (Session, bool, error)
	// End marks the user's session with id in the workspace ended, unless it
	// already is, and in the same write queues the job that writes its
	// summary and, when the workspace has a webhook not stopped, the job that
	// delivers the session to it once the summary's is done or failed. It returns the session as saved, and whether this call ended
	// it: ended again, it keeps the time it first ended and queues nothing.
	// It returns ErrNoSuchSession when there is no such session.
	End(ctx context.Context, workspace, user, id uuid.UUID) (Session, bool, error)
	// Touch marks the user's live session with id in the workspace active
	// now, and says whether there was one to mark.
	Touch(ctx context.Context, workspace, user, id uuid.UUID) (bool, error)
	// UserByReference returns the user a product names by reference in the
	// workspace, if a start has added them. It writes nothing.
	UserByReference(ctx context.Context, workspace uuid.UUID, reference string) (uuid.UUID, bool, error)
	// Reference returns the reference the workspace's product names the user
	// by, if a start from its server added one.
	Reference(ctx context.Context, workspace, user uuid.UUID) (string, bool, error)
	// EndIdle ends every live session, in every workspace, last active at
	// least idle ago, queuing each one's jobs in the same write, as End does, and
	// returns the IDs of those this call ended. A session is active when it
	// starts, when a turn starts in it and when it is touched. Each session is
	// ended once, however many run it at once, and never under a turn starting
	// at the same moment.
	EndIdle(ctx context.Context, idle time.Duration) ([]uuid.UUID, error)
}

// Active marks one of the caller's sessions active, as when the user is
// heard speaking in it, so a long answer doesn't count as being idle. It
// returns ErrSessionEnded once the session has ended.
func (s *Service) Active(ctx context.Context, c access.Caller, id uuid.UUID) error {
	live, err := s.sessions.Touch(ctx, c.Workspace, c.User, id)
	if err != nil {
		return fmt.Errorf("mark the session active: %w", err)
	}
	if !live {
		return ErrSessionEnded
	}
	return nil
}

// EndSession ends one of the caller's sessions and returns it as ended:
// turns are refused from then on, while a turn already in flight finishes
// and is saved, and its summary is written in the background. Ending it
// again changes nothing, so a retried end gets the same answer. A product's
// server may end any session of its workspace, as when a ticket was stolen.
func (s *Service) EndSession(ctx context.Context, c access.Caller, id uuid.UUID) (Session, error) {
	if c.Product {
		session, found, err := s.sessions.InWorkspace(ctx, c.Workspace, id)
		switch {
		case err != nil:
			return Session{}, fmt.Errorf("read the session: %w", err)
		case !found:
			return Session{}, ErrNoSuchSession
		}
		c.User = session.User
	}
	if _, err := s.Session(ctx, c, id); err != nil {
		return Session{}, err
	}
	ended, _, err := s.sessions.End(ctx, c.Workspace, c.User, id)
	if errors.Is(err, ErrNoSuchSession) { // read a moment ago, so only a session removed meanwhile
		return Session{}, err
	}
	if err != nil {
		return Session{}, fmt.Errorf("end the session: %w", err)
	}
	return ended, nil
}

// EndIdle ends every session not active for the idle time: no turn started
// and no speech heard in it, counted from its start for one with neither. It
// returns how many it ended: a user who closed the tab never presses End,
// and the browser can't be relied on to say the tab closed. Every instance
// runs it now and then; a session is ended once however many do.
func (s *Service) EndIdle(ctx context.Context) (int, error) {
	ended, err := s.sessions.EndIdle(ctx, s.settings.SessionIdle)
	if err != nil {
		return 0, fmt.Errorf("end idle sessions: %w", err)
	}
	return len(ended), nil
}

// Session returns one of the caller's sessions, or ErrNoSuchSession for one
// outside their reach.
func (s *Service) Session(ctx context.Context, c access.Caller, id uuid.UUID) (Session, error) {
	session, found, err := s.sessions.ByID(ctx, c.Workspace, c.User, id)
	if err != nil {
		return Session{}, fmt.Errorf("read the session: %w", err)
	}
	if !found {
		return Session{}, ErrNoSuchSession
	}
	return session, nil
}

// StartSession starts a session, for a product's server, with the newest
// version of one of its workspace's agents, with the session context the
// product sent and what that version may read from user context. The same
// request ID with the same agent and session context gives back the session
// it started, as it was, so pressing start twice makes one session; a refused
// or failed start saves nothing, and the request can be retried. The product
// names the user by its own reference, which keeps naming the same user: one
// it names for the first time is added with their first session.
func (s *Service) StartSession(ctx context.Context, c access.Caller, r StartRequest) (Session, bool, error) {
	agent, requestID := r.Agent, r.RequestID
	if err := checkUserReference(r.User); err != nil {
		return Session{}, false, err
	}
	offered, err := s.catalog.Available(ctx, c.Workspace)
	if err != nil {
		return Session{}, false, fmt.Errorf("read the workspace's agents: %w", err)
	}
	i := slices.IndexFunc(offered, func(v agents.Version) bool { return v.ID == agent })
	if i < 0 {
		return Session{}, false, ErrNoSuchAgent
	}
	version := offered[i]
	// newReference is the product's reference for a user it names for the
	// first time, added with their first session.
	var newReference string
	user, found, err := s.sessions.UserByReference(ctx, c.Workspace, r.User)
	switch {
	case err != nil:
		return Session{}, false, fmt.Errorf("%w: %w", ErrUserUnavailable, err)
	case found:
		c.User = user
	default:
		// A provisional ID: Start keeps it unless another start added the
		// reference first, whose user the session is then for. User context
		// is loaded below for this ID, which holds nothing, as the other's
		// does: nothing gives a reference context before its first session.
		// Once something does, the load must follow the user Start settles on.
		c.User, newReference = uuid.NewV7(), r.User
	}
	// A repeat gets its session back without loading again, so a start whose
	// reply was lost can be retried even while user context is down. A user
	// new to voice-guide has started nothing yet.
	if newReference == "" {
		if existing, found, err := s.sessions.ForRequest(ctx, c.Workspace, c.User, requestID); err != nil {
			return Session{}, false, fmt.Errorf("look up the request: %w", err)
		} else if found {
			if err := sameRequest(existing, r); err != nil {
				return Session{}, false, err
			}
			return existing, false, nil
		}
	}
	if err := checkSessionContext(r.SessionContext, version.SessionContext, s.settings.SessionContextChars); err != nil {
		return Session{}, false, err
	}
	items, err := s.loadUserContext(ctx, c, version.ID, version.Reads)
	if err != nil {
		return Session{}, false, err
	}
	started, isNew, err := s.sessions.Start(ctx, Session{
		ID:             uuid.NewV7(),
		Workspace:      c.Workspace,
		User:           c.User,
		Agent:          agent,
		AgentVersion:   version.Number,
		ContextItems:   items,
		SessionContext: slices.Clone(r.SessionContext),
	}, requestID, newReference)
	if err != nil {
		return Session{}, false, fmt.Errorf("save the session: %w", err)
	}
	// A start that lost a race to another with its request ID, for the same
	// user, gets that one's session, which must be for the same request too. A
	// new session is this request's own, so it isn't compared with itself as
	// read back.
	if !isNew {
		if err := sameRequest(started, r); err != nil {
			return Session{}, false, err
		}
	}
	return started, isNew, nil
}

// checkUserReference refuses a start without a user reference within its
// limit. A reference is the product's own stable ID for the user, compared
// exactly as sent, so it is printable characters on one line with no space at
// either end: a name or an email address would change, and the same person
// would become another user.
func checkUserReference(reference string) error {
	const what = "user is the product's own stable ID for the user, not a name or an email address"
	switch {
	case reference == "":
		return fmt.Errorf("%w: a start with a product key names its user: the %s", ErrUserReference, what)
	case !utf8.ValidString(reference), utf8.RuneCountInString(reference) > maxUserReference:
		return fmt.Errorf("%w: at most %d characters of UTF-8; the %s", ErrUserReference, maxUserReference, what)
	case strings.TrimSpace(reference) != reference,
		strings.ContainsFunc(reference, func(r rune) bool { return !unicode.IsPrint(r) }):
		return fmt.Errorf("%w: printable characters on one line, no space at either end; the %s", ErrUserReference, what)
	}
	return nil
}

// sameRequest refuses a repeat that asks for something other than what
// started s: another agent, or session context that differs in any name or
// content, or in order.
func sameRequest(s Session, r StartRequest) error {
	switch {
	case s.Agent != r.Agent:
		return fmt.Errorf("%w with another agent", ErrRequestReused)
	case !slices.Equal(s.SessionContext, r.SessionContext):
		return fmt.Errorf("%w with different session context", ErrRequestReused)
	}
	return nil
}

// loadUserContext returns the IDs of what user context loads of kinds for
// agent, which reads only its own session summaries. An agent that reads
// nothing doesn't ask, so it starts even while user context is down.
func (s *Service) loadUserContext(ctx context.Context, c access.Caller, agent uuid.UUID, kinds []string) ([]uuid.UUID, error) {
	items := []uuid.UUID{}
	if len(kinds) == 0 {
		return items, nil
	}
	loaded, err := s.userContext.Load(ctx, usercontext.LoadRequest{Workspace: c.Workspace, User: c.User, Agent: agent, Kinds: kinds, MaxChars: s.settings.UserContextChars})
	if errors.Is(err, usercontext.ErrBadRequest) {
		// A request user context can't answer is a fault here, not an outage.
		return nil, fmt.Errorf("load the user's context: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUserContextUnavailable, err)
	}
	for _, it := range loaded.Items {
		items = append(items, it.ID)
	}
	return items, nil
}
