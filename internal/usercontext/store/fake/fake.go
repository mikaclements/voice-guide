// Package fake keeps context items in memory: user context's fake for the
// core's tests and for load tests.
package fake

import (
	"context"
	"slices"
	"sync"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/usercontext"
)

// Items keeps context items in memory. It is safe for concurrent use.
type Items struct {
	mu    sync.Mutex
	items []usercontext.Item // in the order added, so the last is the newest
}

// New returns an empty store.
func New() *Items {
	return &Items{}
}

// Add stores it, stamping its creation time.
func (f *Items) Add(it usercontext.Item) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it.CreatedAt = time.Now()
	f.items = append(f.items, it)
}

// Current returns the user's items of the given kinds in the workspace that no
// other item replaces, newest first, the session summaries only those agent wrote.
func (f *Items) Current(_ context.Context, workspace, user uuid.UUID, kinds []string, agent uuid.UUID, limit int) ([]usercontext.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	replaced := map[uuid.UUID]bool{}
	for _, it := range f.items {
		if it.Workspace == workspace {
			replaced[it.Replaces] = true
		}
	}
	var out []usercontext.Item
	for _, it := range slices.Backward(f.items) {
		ours := it.Kind != usercontext.KindSessionSummary || it.Agent == agent
		if it.Workspace == workspace && it.User == user && slices.Contains(kinds, it.Kind) && !replaced[it.ID] && ours {
			out = append(out, it)
		}
	}
	// Newest first, a summary by when its session ended; added later first among equals.
	slices.SortStableFunc(out, func(a, b usercontext.Item) int { return newest(b).Compare(newest(a)) })
	perKind := map[string]int{}
	return slices.DeleteFunc(out, func(it usercontext.Item) bool {
		perKind[it.Kind]++
		return perKind[it.Kind] > limit
	}), nil
}

// newest is the time an item is ordered by: when its session ended, for a
// summary, or when it was added.
func newest(it usercontext.Item) time.Time {
	if !it.SessionEnded.IsZero() {
		return it.SessionEnded
	}
	return it.CreatedAt
}

// AddSummary saves it unless its session already has a summary, and
// returns the session's summary and whether it is this one.
func (f *Items) AddSummary(ctx context.Context, it usercontext.Item) (usercontext.Item, bool, error) {
	if err := ctx.Err(); err != nil {
		return usercontext.Item{}, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, saved := range f.items {
		if saved.Workspace == it.Workspace && saved.Kind == usercontext.KindSessionSummary && saved.Session == it.Session && saved.Replaces == uuid.Nil() {
			return saved, false, nil
		}
	}
	it.CreatedAt = time.Now()
	f.items = append(f.items, it)
	return it, true, nil
}

// SessionSummary returns the user's summary of session in the workspace that
// no other item replaces, if it has one.
func (f *Items) SessionSummary(ctx context.Context, workspace, user, session uuid.UUID) (usercontext.Item, bool, error) {
	if err := ctx.Err(); err != nil {
		return usercontext.Item{}, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.items) - 1; i >= 0; i-- { // newest first
		it := f.items[i]
		if it.Workspace == workspace && it.User == user && it.Kind == usercontext.KindSessionSummary && it.Session == session &&
			!slices.ContainsFunc(f.items, func(newer usercontext.Item) bool { return newer.Replaces == it.ID }) {
			return it, true, nil
		}
	}
	return usercontext.Item{}, false, nil
}

// ByID returns those of ids that are the user's items in the workspace,
// replaced or not, in the order added.
func (f *Items) ByID(_ context.Context, workspace, user uuid.UUID, ids []uuid.UUID) ([]usercontext.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []usercontext.Item
	for _, it := range f.items {
		if it.Workspace == workspace && it.User == user && slices.Contains(ids, it.ID) {
			out = append(out, it)
		}
	}
	return out, nil
}
