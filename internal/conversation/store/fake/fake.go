// Package fake keeps sessions and their turns in memory: conversation's fake for the core's tests
// and for load tests.
package fake

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/conversation"
)

// Sessions keeps sessions in memory. It is safe for concurrent use.
type Sessions struct {
	mu        sync.Mutex
	byRequest map[request]conversation.Session
	// active is when each session was last active after it started: a turn started, as the
	// turns kept with NewTurnsFor tell it, or it was touched.
	active map[uuid.UUID]time.Time
	now    func() time.Time
	// summaries are the sessions whose summary job an end queued, in order.
	summaries []uuid.UUID
	users     map[userReference]uuid.UUID
}

type request struct{ workspace, user, id uuid.UUID }

type userReference struct {
	workspace uuid.UUID
	reference string
}

// New returns an empty store on the system's clock.
func New() *Sessions {
	return &Sessions{byRequest: make(map[request]conversation.Session), active: make(map[uuid.UUID]time.Time), users: make(map[userReference]uuid.UUID), now: time.Now}
}

// SetClock has the store time sessions and their turns by now, so a test steps it.
func (f *Sessions) SetClock(now func() time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = now
}

// UserByReference returns the user a product names by reference in the workspace, if a start added them.
func (f *Sessions) UserByReference(_ context.Context, workspace uuid.UUID, reference string) (uuid.UUID, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	user, ok := f.users[userReference{workspace, reference}]
	return user, ok, nil
}

// AddReference has the workspace's product name user by reference, as its first start for them
// does: for a test whose user already has what earlier sessions left.
func (f *Sessions) AddReference(workspace, user uuid.UUID, reference string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[userReference{workspace, reference}] = user
}

// Reference returns the reference the workspace's product names the user by, if a start added one.
func (f *Sessions) Reference(_ context.Context, workspace, user uuid.UUID) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ref, u := range f.users {
		if ref.workspace == workspace && u == user {
			return ref.reference, true, nil
		}
	}
	return "", false, nil
}

// ForRequest returns the session the user started with requestID in the workspace.
func (f *Sessions) ForRequest(_ context.Context, workspace, user, requestID uuid.UUID) (conversation.Session, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byRequest[request{workspace, user, requestID}]
	return s, ok, nil
}

// Start saves s unless its user already started a session with requestID, adding the reference
// for s's user unless another start added it first, whose user the session is then for.
func (f *Sessions) Start(_ context.Context, s conversation.Session, requestID uuid.UUID, reference string) (conversation.Session, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if reference != "" {
		ref := userReference{s.Workspace, reference}
		if existing, ok := f.users[ref]; ok {
			s.User = existing
		} else {
			f.users[ref] = s.User
		}
	}
	key := request{s.Workspace, s.User, requestID}
	if existing, ok := f.byRequest[key]; ok {
		return existing, false, nil
	}
	s.StartedAt = f.now()
	f.byRequest[key] = s
	return s, true, nil
}

// ByID returns the user's session with id in the workspace.
func (f *Sessions) ByID(_ context.Context, workspace, user, id uuid.UUID) (conversation.Session, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key, ok := f.find(workspace, id)
	if !ok || key.user != user {
		return conversation.Session{}, false, nil
	}
	return f.byRequest[key], true, nil
}

// InWorkspace returns the session with id in the workspace, whichever user's it is.
func (f *Sessions) InWorkspace(_ context.Context, workspace, id uuid.UUID) (conversation.Session, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key, ok := f.find(workspace, id)
	if !ok {
		return conversation.Session{}, false, nil
	}
	return f.byRequest[key], true, nil
}

// ListForUser returns up to limit of the user's sessions in the workspace, newest first, those
// started before the session before, or from the newest for the nil UUID.
func (f *Sessions) ListForUser(_ context.Context, workspace, user, before uuid.UUID, limit int) ([]conversation.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var list []conversation.Session
	for _, s := range f.byRequest {
		if s.Workspace == workspace && s.User == user && (before == uuid.Nil() || s.ID.String() < before.String()) {
			list = append(list, s)
		}
	}
	// UUIDv7 text sorts as the time the ID was made.
	slices.SortFunc(list, func(a, b conversation.Session) int { return strings.Compare(b.ID.String(), a.ID.String()) })
	if len(list) > limit {
		list = list[:limit]
	}
	return list, nil
}

// End marks the user's session with id ended, unless it already is,
// queuing its summary job, and says whether this call ended it.
func (f *Sessions) End(ctx context.Context, workspace, user, id uuid.UUID) (conversation.Session, bool, error) {
	if err := ctx.Err(); err != nil {
		return conversation.Session{}, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key, ok := f.find(workspace, id)
	if !ok || key.user != user {
		return conversation.Session{}, false, conversation.ErrNoSuchSession
	}
	s := f.byRequest[key]
	if !s.EndedAt.IsZero() {
		return s, false, nil
	}
	s.EndedAt = f.now()
	f.byRequest[key] = s
	f.summaries = append(f.summaries, s.ID)
	return s, true, nil
}

// QueuedSummaries returns the sessions whose summary job an end queued, in order.
func (f *Sessions) QueuedSummaries() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uuid.UUID(nil), f.summaries...)
}

// Touch marks the user's live session active now.
func (f *Sessions) Touch(ctx context.Context, workspace, user, id uuid.UUID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key, ok := f.find(workspace, id)
	if !ok || key.user != user || !f.byRequest[key].EndedAt.IsZero() {
		return false, nil
	}
	f.active[id] = f.now()
	return true, nil
}

// EndIdle ends every live session last active at least idle ago.
func (f *Sessions) EndIdle(ctx context.Context, idle time.Duration) ([]uuid.UUID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	now, ended := f.now(), []uuid.UUID{}
	for key, s := range f.byRequest {
		last := s.StartedAt
		if active := f.active[s.ID]; active.After(last) {
			last = active
		}
		if !s.EndedAt.IsZero() || now.Sub(last) < idle {
			continue
		}
		s.EndedAt = now
		f.byRequest[key] = s
		f.summaries = append(f.summaries, s.ID)
		ended = append(ended, s.ID)
	}
	return ended, nil
}

// LastActive returns when the session with id was last active after it
// started, or the zero time if it hasn't been.
func (f *Sessions) LastActive(id uuid.UUID) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[id]
}

// Count returns how many sessions were started.
func (f *Sessions) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.byRequest)
}

// ended says whether the session with id in the workspace has ended.
func (f *Sessions) ended(workspace, id uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	key, ok := f.find(workspace, id)
	return ok && !f.byRequest[key].EndedAt.IsZero()
}

// turned notes that a turn of the session started now.
func (f *Sessions) turned(id uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active[id] = f.now()
}

// find returns the key of the session with id in the workspace. The caller holds mu.
func (f *Sessions) find(workspace, id uuid.UUID) (request, bool) {
	for key, s := range f.byRequest {
		if s.ID == id && s.Workspace == workspace {
			return key, true
		}
	}
	return request{}, false
}
