package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	agentsfake "github.com/mikaclements/voice-guide/internal/agents/store/fake"
	"github.com/mikaclements/voice-guide/internal/conversation"
	modelfake "github.com/mikaclements/voice-guide/internal/conversation/model/fake"
	conversationfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	webhookfake "github.com/mikaclements/voice-guide/internal/conversation/webhook/fake"
	"github.com/mikaclements/voice-guide/internal/jobs"
	jobsfake "github.com/mikaclements/voice-guide/internal/jobs/store/fake"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	itemsfake "github.com/mikaclements/voice-guide/internal/usercontext/store/fake"
)

func TestASummaryTheModelServiceHasNoPlaceForIsLetGoWithoutUsingARun(t *testing.T) {
	workspace, keeper := uuid.NewV7(), uuid.NewV7()
	versions := agentsfake.New()
	if _, err := agents.Publish(context.Background(), versions, workspace, []agents.Agent{{ID: keeper, Name: "Lighthouse keeper", Persona: "You keep the lamp.", Summary: "Note what was covered."}}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	sessions := conversationfake.New()
	model := &modelfake.Model{Pieces: []string{"Bearing 040."}}
	svc := conversation.NewService(agents.NewCatalog(versions), sessions, conversationfake.NewTurnsFor(sessions), model, usercontext.NewService(itemsfake.New()),
		conversation.Settings{UserContextChars: 1000, SessionContextChars: 1000, ScreenInputChars: 1000, WordsChars: 1000, TurnDeadline: time.Minute, Model: "claude-test"})
	s, _, err := svc.StartSession(context.Background(), access.Caller{Workspace: workspace, Product: true}, conversation.StartRequest{Agent: keeper, RequestID: uuid.NewV7(), User: "aiko-42"})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	user := s.User
	caller := access.Caller{Workspace: workspace, User: user}
	if _, err := svc.TakeTurn(context.Background(), caller, conversation.TurnRequest{Session: s.ID, RequestID: uuid.NewV7(), Words: "Is the lamp lit?"}, func(string) {}); err != nil {
		t.Fatalf("TakeTurn: %v", err)
	}
	if _, err := svc.EndSession(context.Background(), caller, s.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	model.Fail, model.Pieces = "busy", nil // the model service is at its own cap

	q := jobsfake.New()
	args, _ := json.Marshal(conversation.SummaryArgs{Session: s.ID})
	q.Add(jobs.Job{ID: uuid.NewV7(), Workspace: workspace, User: user, Kind: conversation.SummaryJob, Args: args})
	settings := jobSettings
	settings.Poll = time.Millisecond
	runner, err := jobs.NewRunner(q, settings, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	jobs.Handle(runner, conversation.SummaryJob, summaryJob(svc))
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	// Stop once the run has let the job go: the claim alone counts an attempt,
	// and the release that gives it back may still be on its way.
	var watching sync.WaitGroup
	watching.Go(func() {
		watch(t, ctx, stop, q, nil, func(q []jobsfake.Queued) bool { return q[0].Runs == 1 && q[0].Attempts == 0 })
	})
	runner.Run(ctx)
	watching.Wait()
	if j := q.All()[0]; j.Runs != 1 || j.Attempts != 0 || j.State != jobsfake.Waiting || !j.RunAt.After(time.Now().Add(jobSettings.BusyWait-time.Second)) {
		t.Errorf("job %+v, want it let go after its run, no attempt used, due again after the busy wait", j)
	}
}

// wiring is what cmd/voice's job runners run over, on fakes: an ended session in a workspace with a
// webhook, a queue, and deliveries through poster.
type wiring struct {
	workspace, user uuid.UUID
	session         conversation.Session
	queue           *jobsfake.Store
	sessions        *conversationfake.Sessions
	webhooks        *conversationfake.Webhooks
	svc             *conversation.Service
	deliveries      *conversation.Deliveries
	// wake, when set, is told of a change outside the queue that done reads.
	wake <-chan struct{}
}

func newWiring(t *testing.T, poster conversation.Poster) wiring {
	t.Helper()
	w := wiring{workspace: uuid.NewV7(), user: uuid.NewV7(), queue: jobsfake.New()}
	sessions, webhooks := conversationfake.New(), conversationfake.NewWebhooks()
	w.sessions, w.webhooks = sessions, webhooks
	w.session, _, _ = sessions.Start(context.Background(), conversation.Session{ID: uuid.NewV7(), Workspace: w.workspace, User: w.user, Agent: uuid.NewV7(), AgentVersion: 1}, uuid.NewV7(), "aiko-42")
	if _, _, err := sessions.End(context.Background(), w.workspace, w.user, w.session.ID); err != nil {
		t.Fatalf("End: %v", err)
	}
	if _, _, err := conversation.SetWebhook(context.Background(), webhooks, w.workspace, "https://jobs.example/webhooks"); err != nil {
		t.Fatalf("SetWebhook: %v", err)
	}
	items := usercontext.NewService(itemsfake.New())
	w.svc = conversation.NewService(agents.NewCatalog(agentsfake.New()), sessions, conversationfake.NewTurnsFor(sessions), &modelfake.Model{}, items,
		conversation.Settings{UserContextChars: 1000, SessionContextChars: 1000, ScreenInputChars: 1000, WordsChars: 1000, TurnDeadline: time.Minute, Model: "claude-test"})
	w.deliveries = conversation.NewDeliveries(sessions, conversationfake.NewTurnsFor(sessions), webhooks, items, poster)
	return w
}

// queue adds a job of kind for the session to the queue, with its args.
func (w wiring) add(kind string, args any) {
	raw, _ := json.Marshal(args)
	w.queue.Add(jobs.Job{ID: uuid.NewV7(), Workspace: w.workspace, User: w.user, Kind: kind, Args: raw})
}

// run runs cmd/voice's runners, on settings that poll every millisecond, until done says to stop,
// failing the test if 5 s pass first.
func (w wiring) run(t *testing.T, summaries, deliveries jobs.Settings, done func([]jobsfake.Queued) bool) {
	t.Helper()
	summaries.Poll, deliveries.Poll = time.Millisecond, time.Millisecond
	runners, err := jobRunners(w.queue, summaries, deliveries, w.svc, w.deliveries, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("jobRunners: %v", err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	var all sync.WaitGroup // the runners and the watch that stops them, each waited for
	all.Go(func() { watch(t, ctx, stop, w.queue, w.wake, done) })
	for _, r := range runners {
		all.Go(func() { r.Run(ctx) })
	}
	all.Wait()
}

// watch calls stop once done holds for the queue, checking at each change to it and each word
// on wake, and fails the test if ctx ends first.
func watch(t *testing.T, ctx context.Context, stop func(), q *jobsfake.Store, wake <-chan struct{}, done func([]jobsfake.Queued) bool) {
	defer stop()
	for {
		queued, changed := q.Watch()
		if done(queued) {
			return
		}
		select {
		case <-changed:
		case <-wake:
		case <-ctx.Done():
			t.Errorf("the jobs never reached what the test waits for; queue %+v", queued)
			return
		}
	}
}

func TestADeliveryToAURLKeptBeforeTheRuleFailsAtOnce(t *testing.T) {
	poster := webhookfake.New()
	w := newWiring(t, poster)
	w.add(conversation.DeliveryJob, conversation.DeliveryArgs{Session: w.session.ID, Delivery: uuid.NewV7()})
	if _, err := w.webhooks.SetURL(context.Background(), w.workspace, "http://jobs.example/webhooks", conversation.NewWebhookSecret("")); err != nil {
		t.Fatalf("SetURL: %v", err)
	}
	w.run(t, jobSettings, deliverySettings, func(q []jobsfake.Queued) bool { return q[0].LastError != "" })
	if j := w.queue.All()[0]; j.State != jobsfake.Failed || j.Attempts != 1 || len(poster.Posted()) != 0 {
		t.Errorf("job %+v, with %d posts; want it failed at its first attempt, nothing posted", j, len(poster.Posted()))
	}
}

func TestADeliveryToAWebhookGoneFailsAtOnceAndAnyOtherRefusalRunsAgain(t *testing.T) {
	for name, tt := range map[string]struct {
		status int
		want   jobsfake.State
	}{
		"gone, 410":          {410, jobsfake.Failed},
		"refused now, 401":   {401, jobsfake.Waiting},
		"not taken yet, 503": {503, jobsfake.Waiting},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWiring(t, webhookfake.New(webhookfake.Answer{Status: tt.status}))
			w.add(conversation.DeliveryJob, conversation.DeliveryArgs{Session: w.session.ID, Delivery: uuid.NewV7()})
			w.run(t, jobSettings, deliverySettings, func(q []jobsfake.Queued) bool { return q[0].LastError != "" })
			if j := w.queue.All()[0]; j.State != tt.want || j.Attempts != 1 || !strings.Contains(j.LastError, strconv.Itoa(tt.status)) {
				t.Errorf("job %+v, want it %s after its first attempt, saying what the product's server answered", j, tt.want)
			}
		})
	}
}

// slowProduct is a product's server that answers nothing until the attempt is given up. It tells
// posted of each post it starts waiting on.
type slowProduct struct {
	waiting atomic.Int32
	posted  chan struct{}
}

func (p *slowProduct) Post(ctx context.Context, _ conversation.Delivery) (int, error) {
	p.waiting.Add(1)
	defer p.waiting.Add(-1)
	select {
	case p.posted <- struct{}{}:
	default: // a word is already waiting, and the watch reads the count fresh
	}
	<-ctx.Done()
	return 0, ctx.Err()
}

func TestASummaryIsClaimedWhileEveryDeliveryWorkerIsStuckOnASlowProduct(t *testing.T) {
	product := &slowProduct{posted: make(chan struct{}, 1)}
	w := newWiring(t, product)
	w.wake = product.posted
	for range deliverySettings.Workers + 1 {
		w.add(conversation.DeliveryJob, conversation.DeliveryArgs{Session: w.session.ID, Delivery: uuid.NewV7()})
	}
	w.add(conversation.SummaryJob, conversation.SummaryArgs{Session: w.session.ID})
	stuck := 0
	w.run(t, jobSettings, deliverySettings, func(q []jobsfake.Queued) bool {
		if n := int(product.waiting.Load()); n > stuck {
			stuck = n
		}
		return stuck == deliverySettings.Workers && q[len(q)-1].State == jobsfake.Done
	})
	if q := w.queue.All(); stuck != deliverySettings.Workers || q[len(q)-1].State != jobsfake.Done {
		t.Fatalf("%d deliveries stuck at once and the summary %s; want every delivery worker stuck, %d, and the summary done", stuck, q[len(q)-1].State, deliverySettings.Workers)
	}
	for _, j := range w.queue.All()[:deliverySettings.Workers+1] {
		if j.State == jobsfake.Failed {
			t.Errorf("delivery %+v failed; want each waiting, never run by the summaries' runner", j)
		}
	}
}

func TestADeliveryIsTriedMoreTimesThanASummaryBeforeItFails(t *testing.T) {
	poster := webhookfake.New(repeated(webhookfake.Answer{Status: 503}, 100)...) // down all along
	w := newWiring(t, poster)
	// Each read of the queue's clock is two days on, past any wait, so a job retried is due again
	// at the next claim.
	var days atomic.Int64
	w.queue.SetClock(func() time.Time { return time.Unix(0, 0).Add(time.Duration(days.Add(2)) * 24 * time.Hour) })
	w.add(conversation.DeliveryJob, conversation.DeliveryArgs{Session: w.session.ID, Delivery: uuid.NewV7()})
	one := deliverySettings
	one.Workers = 1 // one at a time, so the clock running ahead lets no lease run out under a run
	w.run(t, jobSettings, one, func(q []jobsfake.Queued) bool { return q[0].State != jobsfake.Waiting })
	if j, posts := w.queue.All()[0], len(poster.Posted()); j.State != jobsfake.Failed || posts != deliverySettings.Attempts || posts <= jobSettings.Attempts {
		t.Errorf("job %s after %d posts; want it failed after %d, the deliveries' attempts, more than a summary's %d", j.State, posts, deliverySettings.Attempts, jobSettings.Attempts)
	}
}

func repeated[T any](v T, n int) []T {
	out := make([]T, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestASweepRunsAtOnceThenEveryInterval(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	ran := make(chan struct{}, 1)
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		sweep(ctx, time.Hour, "old jobs deleted", func(context.Context) (int, error) {
			calls.Add(1)
			ran <- struct{}{}
			return 0, nil
		}, slog.New(slog.DiscardHandler))
	}()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep didn't run at once, as the prune at start needs")
	}
	stop()
	<-done
	if n := calls.Load(); n != 1 {
		t.Errorf("the sweep ran %d times within its hour, want once, at once", n)
	}
}

func TestTheSettleJobSettlesASummaryLeftPendingOnTheSummariesRunner(t *testing.T) {
	w := newWiring(t, webhookfake.New())
	w.add(conversation.SummarySettleJob, conversation.SummaryArgs{Session: w.session.ID})
	w.run(t, jobSettings, deliverySettings, func(q []jobsfake.Queued) bool { return q[0].State != jobsfake.Waiting })
	s, _, _ := w.sessions.ByID(context.Background(), w.workspace, w.user, w.session.ID)
	if j := w.queue.All()[0]; j.State != jobsfake.Done || s.SummaryStatus != conversation.SummaryNone {
		t.Errorf("job %s, status %q; want the job done and the summary, never written, settled none", j.State, s.SummaryStatus)
	}
}
