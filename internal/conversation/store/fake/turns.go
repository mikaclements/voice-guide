package fake

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/conversation"
)

// Turns keeps turns in memory. A turn in flight holds up its session until it
// finishes, however long that takes: nothing here crashes mid-turn, so none is
// ever left in flight. Like a database, it refuses a write whose context is
// done. It is safe for concurrent use.
type Turns struct {
	mu     sync.Mutex
	turns  []conversation.Turn
	stages map[uuid.UUID][]conversation.Stage
	// changed is closed, and replaced, each time a turn is saved or removed.
	changed chan struct{}
	// sessions, when set, says which sessions have ended and hears when each
	// turn starts, for ending idle sessions.
	sessions *Sessions
}

// NewTurns returns an empty store of turns that doesn't know the sessions, so
// it never refuses a turn as its session ended.
func NewTurns() *Turns {
	return &Turns{stages: make(map[uuid.UUID][]conversation.Stage), changed: make(chan struct{})}
}

// NewTurnsFor returns an empty store of the turns of sessions: it refuses a
// turn whose session has ended, and marks the session active when a turn
// starts. Unlike the database, the check and the turn's start are two
// steps, so an end racing a start may let the turn in, as one landing just
// after it would.
func NewTurnsFor(sessions *Sessions) *Turns {
	t := NewTurns()
	t.sessions = sessions
	return t
}

// Start saves t in flight, numbered after its session's last turn, unless
// another is in flight, one finished after the turn numbered after, the
// session already has a turn with t's request ID, t is an opening and the
// session has any turn, or, kept for sessions, the session has ended.
func (f *Turns) Start(ctx context.Context, t conversation.Turn, after int, _ time.Duration) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if f.sessions != nil && f.sessions.ended(t.Workspace, t.Session) {
		return 0, f.endedFor(t)
	}
	seq, err := f.start(t, after)
	if err == nil && f.sessions != nil {
		f.sessions.turned(t.Session)
	}
	return seq, err
}

// endedFor answers a turn refused as its session ended: a retry of a turn
// the session took is answered as a repeat would be, one in flight as busy.
func (f *Turns) endedFor(t conversation.Turn) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repeatOr(t, conversation.ErrSessionEnded)
}

// repeatOr answers a turn refused for why, unless it retries a turn the
// session took: then as a repeat would be, one in flight as busy. f.mu is
// held.
func (f *Turns) repeatOr(t conversation.Turn, why error) error {
	for _, saved := range f.turns {
		if saved.Workspace == t.Workspace && saved.Session == t.Session && saved.RequestID == t.RequestID {
			if saved.Status == conversation.TurnInFlight {
				return conversation.ErrTurnInFlight
			}
			return conversation.ErrTurnRepeated
		}
	}
	return why
}

func (f *Turns) start(t conversation.Turn, after int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t.Opening && slices.ContainsFunc(f.turns, func(saved conversation.Turn) bool {
		return saved.Workspace == t.Workspace && saved.Session == t.Session
	}) {
		return 0, f.repeatOr(t, conversation.ErrOpeningRefused)
	}
	t.Seq, t.Status = 1, conversation.TurnInFlight
	repeated := false
	for _, saved := range f.turns {
		if saved.Workspace == t.Workspace && saved.Session == t.Session {
			if saved.Status == conversation.TurnInFlight || saved.Seq > after {
				return 0, conversation.ErrTurnInFlight
			}
			t.Seq, repeated = saved.Seq+1, repeated || saved.RequestID == t.RequestID
		}
	}
	if repeated {
		return 0, conversation.ErrTurnRepeated
	}
	f.turns = append(f.turns, t)
	f.change()
	return t.Seq, nil
}

// Finish saves how the turn in flight ended.
func (f *Turns) Finish(ctx context.Context, t conversation.Turn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.turns, func(saved conversation.Turn) bool {
		return saved.Workspace == t.Workspace && saved.ID == t.ID && saved.Status == conversation.TurnInFlight
	})
	if i < 0 {
		return errors.New("no such turn in flight")
	}
	f.turns[i].Status, f.turns[i].Reply = t.Status, t.Reply
	f.change()
	return nil
}

// Cut saves that the user heard only the first t.Heard bytes of an answered
// turn's reply, unless its saved reply starts otherwise or a later turn has
// followed it.
func (f *Turns) Cut(ctx context.Context, t conversation.Turn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.turns, func(saved conversation.Turn) bool {
		return saved.Workspace == t.Workspace && saved.ID == t.ID && saved.Status == conversation.TurnAnswered &&
			strings.HasPrefix(saved.Reply, t.Reply[:t.Heard])
	})
	if i < 0 || slices.ContainsFunc(f.turns, func(later conversation.Turn) bool {
		return later.Session == f.turns[i].Session && later.Seq > f.turns[i].Seq
	}) {
		return errors.New("no such answered turn starting with the part heard, or a later turn followed it")
	}
	f.turns[i].Status, f.turns[i].Heard = conversation.TurnCut, t.Heard
	f.change()
	return nil
}

// Remove deletes the turn t while it is in flight.
func (f *Turns) Remove(ctx context.Context, t conversation.Turn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.turns, func(saved conversation.Turn) bool {
		return saved.Workspace == t.Workspace && saved.ID == t.ID && saved.Status == conversation.TurnInFlight
	})
	if i < 0 {
		return errors.New("no such turn in flight")
	}
	f.turns = slices.Delete(f.turns, i, i+1)
	f.change()
	return nil
}

// Finished returns the session's turns no longer in flight, oldest first.
func (f *Turns) Finished(_ context.Context, workspace, session uuid.UUID) ([]conversation.Turn, error) {
	return slices.DeleteFunc(f.All(), func(t conversation.Turn) bool {
		return t.Workspace != workspace || t.Session != session || t.Status == conversation.TurnInFlight
	}), nil
}

// ForRequest returns the status of the session's turn with requestID, if any.
func (f *Turns) ForRequest(_ context.Context, workspace, session, requestID uuid.UUID) (conversation.TurnStatus, bool, error) {
	for _, t := range f.All() {
		if t.Workspace == workspace && t.Session == session && t.RequestID == requestID {
			return t.Status, true, nil
		}
	}
	return "", false, nil
}

// InFlight says whether the session has a turn in flight; none is ever
// left in flight here, so the deadline doesn't matter.
func (f *Turns) InFlight(_ context.Context, workspace, session uuid.UUID, _ time.Duration) (bool, error) {
	return slices.ContainsFunc(f.All(), func(t conversation.Turn) bool {
		return t.Workspace == workspace && t.Session == session && t.Status == conversation.TurnInFlight
	}), nil
}

// CutOff saves nothing: no turn is ever left in flight here.
func (f *Turns) CutOff(context.Context, uuid.UUID, uuid.UUID, time.Duration) error { return nil }

// All returns every turn saved, in flight or not, oldest first.
func (f *Turns) All() []conversation.Turn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.turns)
}

// Watch returns every turn saved, as All does, and a channel closed at the
// next turn saved or removed, so a test waits for a turn instead of polling.
func (f *Turns) Watch() ([]conversation.Turn, <-chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.turns), f.changed
}

// change wakes whoever watches the turns. The caller holds mu.
func (f *Turns) change() {
	close(f.changed)
	f.changed = make(chan struct{})
}

// RecordStages saves the stage times of a turn already saved.
func (f *Turns) RecordStages(ctx context.Context, t conversation.Turn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.ContainsFunc(f.turns, func(saved conversation.Turn) bool {
		return saved.Workspace == t.Workspace && saved.ID == t.ID
	}) {
		return errors.New("no such turn")
	}
	// A zero Turns works too, so a test that builds one directly doesn't panic here.
	if f.stages == nil {
		f.stages = map[uuid.UUID][]conversation.Stage{}
	}
	f.stages[t.ID] = slices.Clone(t.Stages)
	return nil
}

// Stages returns the stage times recorded for a turn.
func (f *Turns) Stages(turn uuid.UUID) []conversation.Stage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.stages[turn])
}
