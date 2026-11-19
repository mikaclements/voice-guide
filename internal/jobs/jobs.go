// Package jobs runs background work: work too slow for a request, queued by
// the write that calls for it and run off any request by whichever instance
// claims it first, again after a wait when it fails, until it succeeds or
// runs out of attempts. It imports no module: each module hands it a handler
// for its own kind of job, and only programs in cmd/ wire the two together.
package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"slices"
	"sync"
	"time"
	"uuid"
)

// Job is one run of a queued job: what it is for, its kind, the inputs its
// kind's handler reads, which run of it this is, from 1, and how many of its
// runs count against it, this one included.
type Job struct {
	ID        uuid.UUID
	Workspace uuid.UUID
	// User is the user the job is about, or the nil UUID.
	User    uuid.UUID
	Kind    string
	Args    json.RawMessage
	Run     int
	Attempt int
}

// Store keeps the queue. A run's result is saved only by the run that holds
// the job: once a run's lease has run out and another run has claimed it, the
// first run's result is left out.
type Store interface {
	// Claim holds the waiting job of one of kinds due soonest that no run
	// holds, for lease, counting the attempt, and returns it; false when none
	// is due. Two claims at once never take the same job.
	Claim(ctx context.Context, lease time.Duration, kinds []string) (Job, bool, error)
	// Done marks the job finished.
	Done(ctx context.Context, j Job) error
	// Retry lets go of the job, due again after wait, keeping why it failed.
	Retry(ctx context.Context, j Job, wait time.Duration, why string) error
	// Fail ends the job for good, keeping why.
	Fail(ctx context.Context, j Job, why string) error
	// Release lets go of the job, due again after wait, without counting the
	// run: it was stopped, or what it needed was busy; it didn't fail.
	Release(ctx context.Context, j Job, wait time.Duration) error
	// Prune deletes the jobs of kinds done at least age ago, in every
	// workspace, and returns how many; failed jobs stay, to be looked into.
	Prune(ctx context.Context, age time.Duration, kinds []string) (int, error)
}

// Settings say how the runner runs jobs.
type Settings struct {
	// Workers is how many jobs one instance runs at once.
	Workers int
	// Poll is how long a worker waits after finding no job due before looking again.
	Poll time.Duration
	// Timeout bounds one run.
	Timeout time.Duration
	// Lease is how long a claimed job is held before another run may claim
	// it, as when its instance stopped: longer than Timeout, so a job still
	// running is never claimed twice.
	Lease time.Duration
	// Attempts bounds the runs of one job; after the last fails, so does the job.
	Attempts int
	// FirstWait is the wait after a job's first failed run; each later wait
	// is four times the one before, up to longestWait.
	FirstWait time.Duration
	// KeepDone is how long a job done is kept before Prune deletes it.
	KeepDone time.Duration
	// BusyWait is the shortest wait after a run let go because what it
	// needed was busy; each such wait adds up to as long again, at random, so
	// workers that met the same busy place don't all come back together.
	BusyWait time.Duration
}

// longestWait bounds the wait after one failed run: a day, so a job given
// many attempts still runs at least daily, and four times a wait never
// overflows. A round guess, revisited when a kind needs waits past it.
const longestWait = 24 * time.Hour

// Runner claims jobs of the kinds it has handlers for from the store, and
// runs each with its kind's handler. An instance runs one runner per group of
// kinds that wait on the same thing, so a group that is slow holds up no other.
type Runner struct {
	store    Store
	settings Settings
	log      *slog.Logger
	handlers map[string]handler
}

type handler func(ctx context.Context, j Job) error

// NewRunner returns a runner over the store, with no handlers yet, or
// refuses settings it can't run by.
func NewRunner(store Store, settings Settings, log *slog.Logger) (*Runner, error) {
	if err := settings.check(); err != nil {
		return nil, err
	}
	return &Runner{store: store, settings: settings, log: log, handlers: make(map[string]handler)}, nil
}

func (s Settings) check() error {
	switch {
	case s.Workers < 1 || s.Attempts < 1:
		return fmt.Errorf("job settings: workers %d and attempts %d must each be at least one", s.Workers, s.Attempts)
	case s.Poll <= 0 || s.Timeout <= 0 || s.KeepDone <= 0:
		return fmt.Errorf("job settings: poll %s, timeout %s and keep-done %s must each be positive", s.Poll, s.Timeout, s.KeepDone)
	case s.Lease <= s.Timeout:
		return fmt.Errorf("job settings: the lease %s must outlast a run's timeout %s, or a running job is taken twice", s.Lease, s.Timeout)
	case s.FirstWait < 0 || s.BusyWait < 0:
		return fmt.Errorf("job settings: waits %s and %s can't be negative", s.FirstWait, s.BusyWait)
	case s.FirstWait > longestWait || s.BusyWait > longestWait:
		return fmt.Errorf("job settings: waits %s and %s can't be longer than %s", s.FirstWait, s.BusyWait, longestWait)
	}
	return nil
}

// Handle has r run jobs of kind with fn, which gets each job's workspace, its
// user, and its args decoded into A. Args that don't decode into A, or hold a
// field A lacks, fail the job at once, since no later run would read them any
// differently. Call it before Run: r claims only the kinds it has handlers
// for, so an instance whose build has no handler for a kind leaves its jobs
// to one that does.
func Handle[A any](r *Runner, kind string, fn func(ctx context.Context, workspace, user uuid.UUID, args A) error) {
	r.handlers[kind] = func(ctx context.Context, j Job) error {
		var args A
		dec := json.NewDecoder(bytes.NewReader(j.Args))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&args); err != nil {
			return Permanent(fmt.Errorf("the args don't read as a %s job's: %w", kind, err))
		}
		return fn(ctx, j.Workspace, j.User, args)
	}
}

// permanentError is a failure no later run would avoid.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks err as a failure no later run would avoid, so the job
// fails at once instead of being tried again.
func Permanent(err error) error { return permanentError{err} }

// busyError is a run that found what it needed busy.
type busyError struct{ err error }

func (e busyError) Error() string { return e.err.Error() }
func (e busyError) Unwrap() error { return e.err }

// Busy marks err as what the run needed being busy, not failing: the job is
// let go, without counting the run, to run again after a short wait.
func Busy(err error) error { return busyError{err} }

// Run runs jobs on Workers workers until ctx is done, then waits for each
// job in hand: its run is cancelled, and the job is let go to run again at
// once, by another instance or this one when it starts again.
func (r *Runner) Run(ctx context.Context) {
	var workers sync.WaitGroup
	for range r.settings.Workers {
		workers.Go(func() { r.work(ctx) })
	}
	workers.Wait()
}

func (r *Runner) work(ctx context.Context) {
	for ctx.Err() == nil {
		// After a run that succeeded, the next job at once; after none, or one that failed or
		// found what it needed busy, a poll's wait, so a busy place isn't asked again and again.
		if ran, ok := r.once(ctx); ran && ok {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(r.settings.Poll):
		}
	}
}

// once claims a job that is due and runs it, and says whether there was one
// and whether its run succeeded.
func (r *Runner) once(ctx context.Context) (bool, bool) {
	claimCtx, cancel := context.WithTimeout(ctx, saveTimeout)
	j, found, err := r.store.Claim(claimCtx, r.settings.Lease, r.kinds())
	cancel()
	if err != nil && ctx.Err() == nil {
		r.log.Warn("claim a job", "err", err)
	}
	if err != nil || !found {
		return false, false
	}
	return true, r.run(ctx, j)
}

// kinds are the kinds r has handlers for: the only ones it claims or prunes.
func (r *Runner) kinds() []string { return slices.Sorted(maps.Keys(r.handlers)) }

// saveTimeout bounds claiming a job and saving a run's result, each one row
// update that takes milliseconds, so a stalled database holds a worker no
// longer than this. A round guess, measured once jobs run against a database
// shared with live sessions.
const saveTimeout = 5 * time.Second

// run runs one job with its kind's handler and saves how the run ended,
// even once ctx is done, so a job cut short by a stop is let go at once. It
// says whether the run succeeded.
func (r *Runner) run(ctx context.Context, j Job) bool {
	runCtx, cancel := context.WithTimeout(ctx, r.settings.Timeout)
	err := r.handle(runCtx, j)
	cancel()
	saveCtx, cancelSave := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
	defer cancelSave()
	log := r.log.With("job", j.ID, "kind", j.Kind, "run", j.Run, "attempt", j.Attempt)
	succeeded := err == nil
	var permanent permanentError
	var busy busyError
	switch {
	case err == nil:
		err = r.store.Done(saveCtx, j)
	case ctx.Err() != nil: // stopping: not the job's fault, so not a run counted against it
		log.Info("job let go as the runner stops", "err", err)
		err = r.store.Release(saveCtx, j, 0)
	case errors.As(err, &busy):
		wait := r.settings.BusyWait + rand.N(r.settings.BusyWait+1)
		log.Info("job let go: what it needs is busy", "err", err, "after", wait)
		err = r.store.Release(saveCtx, j, wait)
	case errors.As(err, &permanent), j.Attempt >= r.settings.Attempts:
		log.Error("job failed", "err", err)
		err = r.store.Fail(saveCtx, j, err.Error())
	default:
		wait := r.wait(j.Attempt)
		log.Warn("job failed; it runs again", "err", err, "after", wait)
		err = r.store.Retry(saveCtx, j, wait, err.Error())
	}
	if err != nil {
		// Its lease runs out, and another run takes it.
		log.Warn("save how a job's run ended", "err", err)
	}
	return succeeded
}

// handle runs j with its kind's handler. A run past the last attempt, one
// whose instance stopped on its last, fails without running again.
func (r *Runner) handle(ctx context.Context, j Job) error {
	if j.Attempt > r.settings.Attempts {
		return Permanent(fmt.Errorf("out of attempts: run %d of %d", j.Attempt, r.settings.Attempts))
	}
	h, ok := r.handlers[j.Kind]
	if !ok {
		// Only a store that ignored the kinds asked for hands over another; let it go untouched,
		// since a runner with a handler for it may be running elsewhere.
		return Busy(fmt.Errorf("no handler for jobs of kind %q", j.Kind))
	}
	return h(ctx, j)
}

// Prune deletes the jobs of its kinds done at least KeepDone ago, so each
// runner keeps its own kinds as long as its settings say.
func (r *Runner) Prune(ctx context.Context) (int, error) {
	n, err := r.store.Prune(ctx, r.settings.KeepDone, r.kinds())
	if err != nil {
		return 0, fmt.Errorf("delete old jobs: %w", err)
	}
	return n, nil
}

// wait is the wait after the given attempt fails: FirstWait, then four times
// longer each time, up to longestWait, which it checks before growing, so no
// attempt count overflows it.
func (r *Runner) wait(attempt int) time.Duration {
	w := r.settings.FirstWait
	for range attempt - 1 {
		if w > longestWait/4 {
			return longestWait
		}
		w *= 4
	}
	return w
}
