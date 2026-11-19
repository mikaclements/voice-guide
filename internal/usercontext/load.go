package usercontext

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"
	"uuid"
)

// The kinds of context item user context holds. An agent declares which of them it reads. What a
// product already knows, such as a resume, arrives as session context instead.
const (
	KindProfile        = "profile"
	KindSessionSummary = "session_summary"
)

// Kinds lists every kind user context holds.
func Kinds() []string { return []string{KindProfile, KindSessionSummary} }

// Item is something user context holds about a user. An item never changes: a
// change is a new item that replaces it, so a session can tell that what it
// loaded is no longer current.
type Item struct {
	ID        uuid.UUID
	Workspace uuid.UUID
	User      uuid.UUID
	Kind      string
	Content   string
	// Replaces is the item this one replaced, or the nil UUID.
	Replaces uuid.UUID
	// Agent wrote it, for a session summary, of its session Session, which
	// ended at SessionEnded; the nil UUID and zero time for other kinds.
	Agent, Session uuid.UUID
	SessionEnded   time.Time
	CreatedAt      time.Time
}

// Items keeps the context items.
type Items interface {
	// Current returns the user's items of the given kinds in the workspace
	// that no other item replaces, newest first, a session summary by when its
	// session ended, the session summaries only those agent wrote, and at most
	// limit of each kind, the newest.
	Current(ctx context.Context, workspace, user uuid.UUID, kinds []string, agent uuid.UUID, limit int) ([]Item, error)
	// ByID returns those of ids that are the user's items in the workspace,
	// replaced or not, in any order.
	ByID(ctx context.Context, workspace, user uuid.UUID, ids []uuid.UUID) ([]Item, error)
	// AddSummary saves it, a session summary, unless its session already has
	// one, and returns the session's summary and whether it is this one.
	AddSummary(ctx context.Context, it Item) (Item, bool, error)
	// SessionSummary returns the user's summary of session in the workspace
	// that no other item replaces, if it has one.
	SessionSummary(ctx context.Context, workspace, user, session uuid.UUID) (Item, bool, error)
}

// LoadRequest asks for what an agent may know about a user: the kinds it
// declares, in its order, within a size limit counted in characters. Of the
// session summaries, an agent reads only those it wrote.
type LoadRequest struct {
	Workspace uuid.UUID
	User      uuid.UUID
	Agent     uuid.UUID
	Kinds     []string
	MaxChars  int
}

// Loaded is what a load chose, in the order to show it, and the IDs of the
// current items it read but left out because they didn't fit. Items past the
// row limit of their kind, each older than every item of its kind that was
// read, aren't read, so aren't listed: listing them would mean reading them,
// which the limit is there to spare.
type Loaded struct {
	Items   []Item
	LeftOut []uuid.UUID
}

// ErrBadRequest reports a load request user context can't answer as asked.
var ErrBadRequest = errors.New("bad load request")

// Service is user context's API, which other modules call directly.
type Service struct {
	items Items
}

// NewService returns user context over its store of items.
func NewService(items Items) *Service {
	return &Service{items: items}
}

// Load returns the user's current items of the requested kinds, in their order
// and newest first, whole items only; one that doesn't fit is left out and the next is tried.
func (s *Service) Load(ctx context.Context, r LoadRequest) (Loaded, error) {
	if err := r.check(); err != nil {
		return Loaded{}, err
	}
	var kinds []string
	for _, k := range r.Kinds {
		if !slices.Contains(kinds, k) {
			kinds = append(kinds, k)
		}
	}
	items, err := s.items.Current(ctx, r.Workspace, r.User, kinds, r.Agent, rowsFor(r.MaxChars))
	if err != nil {
		return Loaded{}, fmt.Errorf("read the user's items: %w", err)
	}
	out := Loaded{Items: []Item{}, LeftOut: []uuid.UUID{}}
	room := r.MaxChars
	for _, kind := range kinds {
		for _, it := range items {
			if it.Kind != kind {
				continue
			}
			if n := utf8.RuneCountInString(it.Content); n <= room {
				out.Items = append(out.Items, it)
				room -= n
			} else {
				out.LeftOut = append(out.LeftOut, it.ID)
			}
		}
	}
	return out, nil
}

// rowsFor is how many items of a kind a load reads, newest first, for a
// limit of maxChars: one per 100 characters of it, and at least 20. A summary
// runs to some 900 characters, so at the 20,000-character default that is 200
// rows, ten times the summaries that fit; an older item past them is never
// read, which only very short items in great numbers would notice. A round
// guess, checked when kinds with short items are added.
func rowsFor(maxChars int) int { return max(maxChars/100, 20) }

func (r LoadRequest) check() error {
	switch {
	case r.Workspace == uuid.Nil() || r.User == uuid.Nil():
		return fmt.Errorf("%w: a workspace and a user are required", ErrBadRequest)
	case r.Agent == uuid.Nil() && slices.Contains(r.Kinds, KindSessionSummary):
		return fmt.Errorf("%w: session summaries are read by the agent that wrote them, so an agent is required", ErrBadRequest)
	case r.MaxChars < 1:
		return fmt.Errorf("%w: the limit must be at least one character", ErrBadRequest)
	}
	for _, k := range r.Kinds {
		if !slices.Contains(Kinds(), k) {
			return fmt.Errorf("%w: unknown kind %q", ErrBadRequest, k)
		}
	}
	return nil
}
