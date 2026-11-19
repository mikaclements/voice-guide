package jobs_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/jobs"
	"github.com/mikaclements/voice-guide/internal/jobs/store/fake"
)

var (
	workspace = uuid.MustParse("01a1de8a-0000-7000-8000-000000000001")
	user      = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a1")
)

var settings = jobs.Settings{Workers: 1, Poll: time.Millisecond, Timeout: time.Minute, Lease: 2 * time.Minute, Attempts: 4, FirstWait: 10 * time.Second, KeepDone: time.Hour}

// lamp is a kind of job's args.
type lamp struct {
	Tower string `json:"tower"`
}

// clock is the queue's time, as a test steps it.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) step(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// queue returns a queue on a clock the test steps, holding one job of kind "lamp" with args.
func queue(t *testing.T, args string) (*fake.Store, *clock) {
	t.Helper()
	c := &clock{now: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	q := fake.New()
	q.SetClock(c.Now)
	q.Add(jobs.Job{ID: uuid.NewV7(), Workspace: workspace, User: user, Kind: "lamp", Args: []byte(args)})
	return q, c
}

func TestAJobRunsWithItsArgsAndIsDone(t *testing.T) {
	q, _ := queue(t, `{"tower":"North"}`)
	r := newRunner(t, q, settings)
	var got lamp
	var gotWorkspace, gotUser uuid.UUID
	jobs.Handle(r, "lamp", func(_ context.Context, ws, u uuid.UUID, args lamp) error {
		got, gotWorkspace, gotUser = args, ws, u
		return nil
	})

	if !jobs.RunOnce(r, context.Background()) {
		t.Fatal("no job ran")
	}
	if all := q.All(); got.Tower != "North" || gotWorkspace != workspace || gotUser != user || all[0].State != fake.Done || all[0].Attempts != 1 {
		t.Errorf("ran with %+v for %s, %s; queue %+v; want the args, workspace and user, and the job done on its first run", got, gotWorkspace, gotUser, all)
	}
	if jobs.RunOnce(r, context.Background()) {
		t.Error("a job ran again once done")
	}
}

func TestAFailingJobRunsAgainAfterAWaitFourTimesLongerEachTimeThenFails(t *testing.T) {
	q, c := queue(t, `{"tower":"North"}`)
	r := newRunner(t, q, settings)
	runs := 0
	jobs.Handle(r, "lamp", func(context.Context, uuid.UUID, uuid.UUID, lamp) error {
		runs++
		return errors.New("the lamp won't light")
	})

	for _, wait := range []time.Duration{10 * time.Second, 40 * time.Second, 160 * time.Second} {
		if !jobs.RunOnce(r, context.Background()) {
			t.Fatalf("run %d didn't happen", runs+1)
		}
		c.step(wait - time.Second)
		if jobs.RunOnce(r, context.Background()) {
			t.Fatalf("after run %d, it ran again before its wait of %s", runs, wait)
		}
		c.step(time.Second)
	}
	if !jobs.RunOnce(r, context.Background()) {
		t.Fatal("the last run didn't happen")
	}
	if all := q.All(); runs != settings.Attempts || all[0].State != fake.Failed || all[0].LastError != "the lamp won't light" {
		t.Errorf("ran %d times; queue %+v; want %d runs, then the job failed with why", runs, all, settings.Attempts)
	}
}

func TestArgsThatDontReadFailTheJobAtOnce(t *testing.T) {
	for name, args := range map[string]string{
		"not JSON":                  `lamp`,
		"a field of the wrong type": `{"tower":7}`,
		"a field it lacks":          `{"tower":"North","keeper":"Ada"}`,
	} {
		t.Run(name, func(t *testing.T) {
			q, _ := queue(t, args)
			r := newRunner(t, q, settings)
			ran := false
			jobs.Handle(r, "lamp", func(context.Context, uuid.UUID, uuid.UUID, lamp) error { ran = true; return nil })
			jobs.RunOnce(r, context.Background())
			if all := q.All(); ran || all[0].State != fake.Failed || all[0].Attempts != 1 {
				t.Errorf("ran %v; queue %+v; want it failed on its first run without running", ran, all)
			}
		})
	}
}

func TestAPermanentFailureFailsTheJobAtOnce(t *testing.T) {
	q, _ := queue(t, `{"tower":"North"}`)
	r := newRunner(t, q, settings)
	jobs.Handle(r, "lamp", func(context.Context, uuid.UUID, uuid.UUID, lamp) error {
		return jobs.Permanent(errors.New("no such tower"))
	})
	jobs.RunOnce(r, context.Background())
	if all := q.All(); all[0].State != fake.Failed || all[0].Attempts != 1 {
		t.Errorf("queue %+v, want the job failed after one run", all)
	}
}

func TestARunnerLeavesAKindItHasNoHandlerForWaiting(t *testing.T) {
	q, _ := queue(t, `{"tower":"North"}`) // a lamp, queued by a newer build, say
	r := newRunner(t, q, settings)
	jobs.Handle(r, "beacon", func(context.Context, uuid.UUID, uuid.UUID, lamp) error { return nil })
	if jobs.RunOnce(r, context.Background()) {
		t.Error("a runner with no handler for lamps claimed one")
	}
	if all := q.All(); all[0].State != fake.Waiting || all[0].Runs != 0 {
		t.Errorf("queue %+v, want the lamp waiting, never claimed, for a runner that has its handler", all)
	}
}

func TestAWaitGrowsFourTimesUpToADayWhateverTheAttempt(t *testing.T) {
	r := newRunner(t, fake.New(), settings) // a first wait of 10 s
	for attempt, want := range map[int]time.Duration{
		1:                  10 * time.Second,
		2:                  40 * time.Second,
		7:                  40960 * time.Second, // 11.4 hours, under the ceiling
		8:                  jobs.LongestWait,    // 45.5 hours, over it
		1 << 20:            jobs.LongestWait,
		int(^uint(0) >> 1): jobs.LongestWait,
	} {
		if got := jobs.Wait(r, attempt); got != want {
			t.Errorf("the wait after attempt %d = %s, want %s", attempt, got, want)
		}
	}
}

func TestAJobWhoseLeaseRanOutIsRunAgainAndTheFirstRunsResultIsLeftOut(t *testing.T) {
	q, c := queue(t, `{"tower":"North"}`)
	first, err := func() (bool, error) { // a run claims the job, then its instance stops
		_, found, err := q.Claim(context.Background(), settings.Lease, []string{"lamp"})
		return found, err
	}()
	if !first || err != nil {
		t.Fatalf("Claim = %v, %v", first, err)
	}
	r := newRunner(t, q, settings)
	runs := 0
	jobs.Handle(r, "lamp", func(context.Context, uuid.UUID, uuid.UUID, lamp) error { runs++; return nil })
	c.step(settings.Lease - time.Second)
	if jobs.RunOnce(r, context.Background()) {
		t.Fatal("the job ran again while the first run held it")
	}
	c.step(time.Second)
	if !jobs.RunOnce(r, context.Background()) || runs != 1 {
		t.Fatalf("ran %d times once the lease ran out, want once", runs)
	}
	stale := jobs.Job{ID: q.All()[0].ID, Run: 1, Attempt: 1}
	if err := q.Fail(context.Background(), stale, "late"); err == nil {
		t.Error("the first run, whose lease ran out, saved its result over the second's")
	}
	if all := q.All(); all[0].State != fake.Done || all[0].Attempts != 2 {
		t.Errorf("queue %+v, want the job done by its second run", all)
	}
}

func TestARunPastTheLastAttemptFailsWithoutRunning(t *testing.T) {
	q, c := queue(t, `{"tower":"North"}`)
	for range settings.Attempts { // each run's instance stopped before it ended
		if _, found, err := q.Claim(context.Background(), settings.Lease, []string{"lamp"}); !found || err != nil {
			t.Fatalf("Claim = %v, %v", found, err)
		}
		c.step(settings.Lease)
	}
	r := newRunner(t, q, settings)
	ran := false
	jobs.Handle(r, "lamp", func(context.Context, uuid.UUID, uuid.UUID, lamp) error { ran = true; return nil })
	jobs.RunOnce(r, context.Background())
	if all := q.All(); ran || all[0].State != fake.Failed {
		t.Errorf("ran %v; queue %+v; want it failed without running", ran, all)
	}
}

func TestStoppingCancelsTheJobInHandWaitsForItAndLetsItGo(t *testing.T) {
	q, c := queue(t, `{"tower":"North"}`)
	r := newRunner(t, q, settings)
	started, returned := make(chan struct{}), make(chan struct{})
	jobs.Handle(r, "lamp", func(ctx context.Context, _, _ uuid.UUID, _ lamp) error {
		close(started)
		<-ctx.Done()
		close(returned)
		return ctx.Err()
	})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	<-started
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't return within 5s of being stopped")
	}
	select {
	case <-returned:
	default:
		t.Error("Run returned before the job in hand did")
	}
	if all := q.All(); all[0].State != fake.Waiting || all[0].RunAt.After(c.Now()) || all[0].Attempts != 0 {
		t.Errorf("queue %+v, want the job waiting, due at once, its run not counted", all)
	}
}

func TestEachWorkerRunsAJobAtOnce(t *testing.T) {
	q, _ := queue(t, `{"tower":"North"}`)
	q.Add(jobs.Job{ID: uuid.NewV7(), Workspace: workspace, Kind: "lamp", Args: []byte(`{"tower":"South"}`)})
	two := settings
	two.Workers = 2
	r := newRunner(t, q, two)
	var both sync.WaitGroup
	both.Add(2)
	jobs.Handle(r, "lamp", func(context.Context, uuid.UUID, uuid.UUID, lamp) error {
		both.Done()
		both.Wait() // each run waits for the other to start, so only two at once get here
		return nil
	})
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	go func() {
		both.Wait()
		stop()
	}()
	r.Run(ctx)
	if all := q.All(); all[0].State != fake.Done || all[1].State != fake.Done {
		t.Errorf("queue %+v, want both jobs done, run at once", all)
	}
}

func TestARunnerPrunesOnlyItsOwnKinds(t *testing.T) {
	q, _ := queue(t, `{"tower":"North"}`)
	q.Add(jobs.Job{ID: uuid.NewV7(), Workspace: workspace, Kind: "beacon", Args: []byte(`{"tower":"South"}`)})
	lamps, beacons := newRunner(t, q, settings), newRunner(t, q, settings)
	jobs.Handle(lamps, "lamp", func(context.Context, uuid.UUID, uuid.UUID, lamp) error { return nil })
	jobs.Handle(beacons, "beacon", func(context.Context, uuid.UUID, uuid.UUID, lamp) error { return nil })
	jobs.RunOnce(lamps, context.Background())
	jobs.RunOnce(beacons, context.Background())
	if n, err := lamps.Prune(context.Background()); n != 1 || err != nil {
		t.Errorf("Prune = %d, %v; want the lamp done deleted", n, err)
	}
	if all := q.All(); len(all) != 1 || all[0].Kind != "beacon" || all[0].State != fake.Done {
		t.Errorf("queue %+v, want the beacon done kept, for its own runner to prune", all)
	}
}

func TestAJobOfAKindWithNoHandlerHandedOverAnywayIsLetGoUncounted(t *testing.T) {
	q, c := queue(t, `{"tower":"North"}`)
	waits := settings
	waits.BusyWait = time.Second
	r := newRunner(t, everyKind{q}, waits)
	jobs.Handle(r, "beacon", func(context.Context, uuid.UUID, uuid.UUID, lamp) error { return nil })
	if !jobs.RunOnce(r, context.Background()) {
		t.Fatal("the store handed over nothing")
	}
	if all := q.All(); all[0].State != fake.Waiting || all[0].Attempts != 0 || !all[0].RunAt.After(c.Now()) {
		t.Errorf("queue %+v, want the lamp let go, its run not counted, due again after a wait, for a runner with its handler", all)
	}
}

// everyKind is a queue that hands over a job of any kind, whatever kinds the claim asks for.
type everyKind struct{ *fake.Store }

func (q everyKind) Claim(ctx context.Context, lease time.Duration, _ []string) (jobs.Job, bool, error) {
	return q.Store.Claim(ctx, lease, []string{"lamp", "beacon"})
}

func TestPruneDeletesJobsDoneAndKeepsFailedAndWaitingOnes(t *testing.T) {
	q, _ := queue(t, `{"tower":"North"}`)
	q.Add(jobs.Job{ID: uuid.NewV7(), Workspace: workspace, Kind: "lamp", Args: []byte(`lamp`)})
	q.Add(jobs.Job{ID: uuid.NewV7(), Workspace: workspace, Kind: "lamp", Args: []byte(`{"tower":"South"}`)})
	r := newRunner(t, q, settings)
	jobs.Handle(r, "lamp", func(context.Context, uuid.UUID, uuid.UUID, lamp) error { return nil })
	jobs.RunOnce(r, context.Background()) // the first lamp: done
	jobs.RunOnce(r, context.Background()) // the lamp whose args don't read: failed
	if n, err := r.Prune(context.Background()); n != 1 || err != nil {
		t.Errorf("Prune = %d, %v; want the job done deleted", n, err)
	}
	if all := q.All(); len(all) != 2 || all[0].State != fake.Failed || all[1].State != fake.Waiting {
		t.Errorf("queue %+v, want the failed and the waiting job kept", all)
	}
}

func TestARunThatFindsWhatItNeedsBusyIsLetGoWithoutUsingAnAttempt(t *testing.T) {
	q, c := queue(t, `{"tower":"North"}`)
	busy := settings
	busy.BusyWait = 5 * time.Second
	r := newRunner(t, q, busy)
	jobs.Handle(r, "lamp", func(context.Context, uuid.UUID, uuid.UUID, lamp) error {
		return jobs.Busy(errors.New("the model service is at its cap"))
	})
	for range 2 * settings.Attempts {
		jobs.RunOnce(r, context.Background())
		c.step(2 * busy.BusyWait) // past the longest wait, jitter included
	}
	all := q.All()
	if all[0].State != fake.Waiting || all[0].Attempts != 0 || all[0].Runs != 2*settings.Attempts {
		t.Errorf("queue %+v, want the job still waiting after %d busy runs, none counted", all, 2*settings.Attempts)
	}
	if wait := all[0].RunAt.Sub(c.Now().Add(-2 * busy.BusyWait)); wait < busy.BusyWait || wait > 2*busy.BusyWait {
		t.Errorf("due again %s after its last run, want between %s and %s", wait, busy.BusyWait, 2*busy.BusyWait)
	}
}

func TestTwoWorkersSharingOnePlaceRunEveryJobInTheEnd(t *testing.T) {
	q := fake.New()
	const queued = 8
	for range queued {
		q.Add(jobs.Job{ID: uuid.NewV7(), Workspace: workspace, Kind: "lamp", Args: []byte(`{"tower":"North"}`)})
	}
	two := settings
	two.Workers, two.Attempts, two.Poll, two.BusyWait = 2, 1, time.Millisecond, time.Millisecond
	r := newRunner(t, q, two)
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	place := make(chan struct{}, 1) // the one place, as the model service keeps for background asks
	met := make(chan struct{})      // closed once a worker meets the place taken
	var metOnce sync.Once
	var done atomic.Int32
	jobs.Handle(r, "lamp", func(jobCtx context.Context, _, _ uuid.UUID, _ lamp) error {
		select {
		case place <- struct{}{}:
		default:
			metOnce.Do(func() { close(met) })
			return jobs.Busy(errors.New("the place is taken"))
		}
		// The first job holds the place until the other worker has met it taken.
		select {
		case <-met:
		case <-jobCtx.Done():
		}
		<-place
		if done.Add(1) == queued {
			stop()
		}
		return nil
	})
	r.Run(ctx)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("waited 10s for every job to run; %d of %d done", done.Load(), queued)
	}
	for _, j := range q.All() {
		if j.State != fake.Done {
			t.Errorf("job %+v, want every job done, though each worker only ever had one attempt", j)
		}
	}
}

// newRunner returns a runner over q with settings s, which the test means to be good.
func newRunner(t *testing.T, q jobs.Store, s jobs.Settings) *jobs.Runner {
	t.Helper()
	r, err := jobs.NewRunner(q, s, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r
}

func TestARunnerRefusesSettingsItCantRunBy(t *testing.T) {
	for name, change := range map[string]func(*jobs.Settings){
		"no workers":                 func(s *jobs.Settings) { s.Workers = 0 },
		"no attempts":                func(s *jobs.Settings) { s.Attempts = 0 },
		"no poll":                    func(s *jobs.Settings) { s.Poll = 0 },
		"no timeout":                 func(s *jobs.Settings) { s.Timeout = 0 },
		"a lease within the timeout": func(s *jobs.Settings) { s.Lease = s.Timeout },
		"a negative wait":            func(s *jobs.Settings) { s.FirstWait = -time.Second },
		"a negative busy wait":       func(s *jobs.Settings) { s.BusyWait = -time.Second },
		"a first wait past a day":    func(s *jobs.Settings) { s.FirstWait = jobs.LongestWait + time.Second },
		"a busy wait past a day":     func(s *jobs.Settings) { s.BusyWait = jobs.LongestWait + time.Second },
		"jobs done kept for no time": func(s *jobs.Settings) { s.KeepDone = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			s := settings
			change(&s)
			if _, err := jobs.NewRunner(fake.New(), s, slog.New(slog.DiscardHandler)); err == nil {
				t.Error("NewRunner took the settings")
			}
		})
	}
}
