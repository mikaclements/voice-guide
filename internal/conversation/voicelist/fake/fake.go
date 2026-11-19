// Package fake lists voices from a set of names: conversation's fake of the text-to-speech
// service's voice list, for the core's tests and the HTTP adapter's.
package fake

import (
	"context"
	"slices"
	"sync"
)

// List returns the names it holds, or Err while one is set. It counts the times it was asked.
// It is safe for concurrent use.
type List struct {
	mu    sync.Mutex
	names []string
	err   error
	asked int
	// Block, when set, makes Voices wait for ctx to end before it answers, as a service that
	// stalls does.
	Block bool
}

// New returns a list of names.
func New(names ...string) *List { return &List{names: names} }

// Fail makes the next asks fail with err, until it is called with nil.
func (l *List) Fail(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.err = err
}

// Asked is how many times the list was asked.
func (l *List) Asked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.asked
}

// Voices returns the names, a copy the caller may change.
func (l *List) Voices(ctx context.Context) ([]string, error) {
	l.mu.Lock()
	l.asked++
	names, err, block := slices.Clone(l.names), l.err, l.Block
	l.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return names, err
}
