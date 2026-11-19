// Package fake keeps the job queue in memory: the jobs package's fake for its
// tests and for load tests.
package fake

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/mikaclements/voice-guide/internal/jobs"
)

// State is where a job stands.
type State string

const (
	Waiting State = "waiting"
	Done    State = "done"
	Failed  State = "failed"
)

// Queued is a job as the queue keeps it.
type Queued struct {
	jobs.Job
	State     State
	RunAt     time.Time
	Runs      int
	Attempts  int
	LastError string
}

// Store keeps jobs in memory, on a clock a test can set. It is safe for
// concurrent use.
type Store struct {
	mu      sync.Mutex
	jobs    []Queued
	now     func() time.Time
	changed chan struct{} // closed, and replaced, at each change to a job
}

// New returns an empty queue on the system's clock.
func New() *Store { return &Store{now: time.Now, changed: make(chan struct{})} }

// SetClock has the queue tell the time by now, so a test steps it.
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// Add queues j, due at once.
func (s *Store) Add(j jobs.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, Queued{Job: j, State: Waiting, RunAt: s.now()})
	s.change()
}

// All returns every job, in the order queued.
func (s *Store) All() []Queued {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.jobs)
}

// Watch returns every job, as All does, and a channel closed at the next
// change to any, so a test waits for a job instead of polling.
func (s *Store) Watch() ([]Queued, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.jobs), s.changed
}

// change wakes whoever watches the queue. The caller holds mu.
func (s *Store) change() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// Claim holds the waiting job of one of kinds due soonest, for lease.
func (s *Store) Claim(ctx context.Context, lease time.Duration, kinds []string) (jobs.Job, bool, error) {
	if err := ctx.Err(); err != nil {
		return jobs.Job{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now, due := s.now(), -1
	for i, q := range s.jobs {
		if q.State == Waiting && slices.Contains(kinds, q.Kind) && !q.RunAt.After(now) && (due < 0 || q.RunAt.Before(s.jobs[due].RunAt)) {
			due = i
		}
	}
	if due < 0 {
		return jobs.Job{}, false, nil
	}
	q := &s.jobs[due]
	q.Runs++
	q.Attempts++
	q.RunAt = now.Add(lease)
	j := q.Job
	j.Run, j.Attempt = q.Runs, q.Attempts
	s.change()
	return j, true, nil
}

var errNotHeld = errors.New("the job isn't held by this run")

// Done marks the job finished, if this run holds it.
func (s *Store) Done(ctx context.Context, j jobs.Job) error {
	return s.settle(ctx, j, func(q *Queued) { q.State = Done })
}

// Retry lets go of the job, due again after wait.
func (s *Store) Retry(ctx context.Context, j jobs.Job, wait time.Duration, why string) error {
	return s.settle(ctx, j, func(q *Queued) { q.RunAt, q.LastError = s.now().Add(wait), why })
}

// Fail ends the job for good.
func (s *Store) Fail(ctx context.Context, j jobs.Job, why string) error {
	return s.settle(ctx, j, func(q *Queued) { q.State, q.LastError = Failed, why })
}

// Release lets go of the job, due again after wait, without counting the run.
func (s *Store) Release(ctx context.Context, j jobs.Job, wait time.Duration) error {
	return s.settle(ctx, j, func(q *Queued) { q.RunAt, q.Attempts = s.now().Add(wait), q.Attempts-1 })
}

// Prune deletes the jobs of kinds done at least age ago; the fake keeps no
// finish time, so it deletes every job of kinds done.
func (s *Store) Prune(ctx context.Context, _ time.Duration, kinds []string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	before := len(s.jobs)
	s.jobs = slices.DeleteFunc(s.jobs, func(q Queued) bool { return q.State == Done && slices.Contains(kinds, q.Kind) })
	s.change()
	return before - len(s.jobs), nil
}

func (s *Store) settle(ctx context.Context, j jobs.Job, change func(*Queued)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.jobs {
		if q := &s.jobs[i]; q.ID == j.ID && q.State == Waiting && q.Runs == j.Run {
			change(q)
			s.change()
			return nil
		}
	}
	return errNotHeld
}
