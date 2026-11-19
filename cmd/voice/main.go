// Command voice runs the voice service: it wires the conversation, speech, usercontext
// and agents modules together. Only programs in cmd/ wire modules.
// Settings come from the environment; scripts/run.sh sets them for local development.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/agents"
	agentsstore "github.com/mikaclements/voice-guide/internal/agents/store/postgres"
	"github.com/mikaclements/voice-guide/internal/api"
	"github.com/mikaclements/voice-guide/internal/conversation"
	conversationhttp "github.com/mikaclements/voice-guide/internal/conversation/http"
	modelhttp "github.com/mikaclements/voice-guide/internal/conversation/model/http"
	conversationstore "github.com/mikaclements/voice-guide/internal/conversation/store/postgres"
	webhookhttp "github.com/mikaclements/voice-guide/internal/conversation/webhook/http"
	"github.com/mikaclements/voice-guide/internal/jobs"
	jobstore "github.com/mikaclements/voice-guide/internal/jobs/store/postgres"
	"github.com/mikaclements/voice-guide/internal/productkey"
	productkeystore "github.com/mikaclements/voice-guide/internal/productkey/store/postgres"
	"github.com/mikaclements/voice-guide/internal/speech"
	detectionfake "github.com/mikaclements/voice-guide/internal/speech/detection/fake"
	"github.com/mikaclements/voice-guide/internal/speech/detection/silero"
	speechhttp "github.com/mikaclements/voice-guide/internal/speech/http"
	webrtctransport "github.com/mikaclements/voice-guide/internal/speech/transport/webrtc"
	ttsfake "github.com/mikaclements/voice-guide/internal/speech/tts/fake"
	"github.com/mikaclements/voice-guide/internal/speech/tts/kokoro"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	usercontextstore "github.com/mikaclements/voice-guide/internal/usercontext/store/postgres"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(context.Background(), os.Getenv, log); err != nil {
		log.Error("voice stopped", "err", err)
		os.Exit(1)
	}
}

// defaultContextLimit is about 5,000 tokens of English: room for past session summaries in user
// context; tuned once turns are timed.
const defaultContextLimit = 20_000

// defaultSessionContextLimit is about 100,000 tokens, at the 2.8 characters a token measured on a
// Markdown document: room for long documents. Measured on the subscription with Sonnet 5.5, an
// opening reached its first text in 1.4 to 3.9 s cold with 120,000 characters, and in 1.3 to
// 2.4 s with 400,000, both well inside the model service's 15 s and the turn's 20 s. Not 400,000:
// its some 141,000 tokens leave too little of a 200,000-token context for a long conversation.
const defaultSessionContextLimit = 280_000

// defaultScreenInputLimit is about 10,000 tokens of English with a turn: room for a whiteboard of
// boxes and arrows, or a document the user is reading, turned into text. Only the newest block of
// each name is read fresh each turn, the earlier ones standing as one line in the cached
// conversation, but that block is read fresh on every turn. Estimated from published prompt
// caching figures (about 0.09 s more to the first word per 1,000 tokens read fresh rather than
// from cache), a full 10,000-token board adds about 0.9 s before the first word, and a board of
// about 1,000 tokens, the size expected, about 0.1 s. A round guess, measured with a 1,000-token
// board on the real page before the apps that send boards ship.
const defaultScreenInputLimit = 40_000

// defaultWordsLimit is about 700 words of English in one turn: room for a long answer, while one
// pasted document can't make a turn slow or costly.
const defaultWordsLimit = 4_000

// defaultMaxAudio bounds the audio connections one server holds. Measured on an M3 Pro, a
// connection that never connects costs about 225 KB, 24 goroutines and a socket per address
// until it times out after about 30s, so 100 keeps someone with a valid ticket opening many to
// about 23 MB; and it sits well above the sessions one server's text-to-speech can voice (Kokoro
// on CPU runs about 4 times faster than speech), so no real user meets it first.
const defaultMaxAudio = 100

// defaultTurnTimeout is the longest a turn runs unless set. A whole reply takes about 2 to 2.5s
// (measured on Sonnet 5.5), so a turn still running at 20s means something is wrong, and cutting
// it off is better than keeping the user waiting. It is also how long a turn left in flight by a
// crashed server holds up its session, which a short deadline keeps short.
const defaultTurnTimeout = 20 * time.Second

// defaultSessionIdle is how long a session goes with no turn and no speech before it ends, unless set: 5
// minutes, Pipecat's default idle timeout, long enough for someone to stop and think and short
// enough that a closed tab's session is soon summarised. A starting value, checked once real
// sessions show how long people pause.
const defaultSessionIdle = 5 * time.Minute

// sessionSweep is how often each instance ends idle sessions: once a minute, so a session ends at
// most a minute past its idle time, for one query on an index of live sessions. A round guess.
const sessionSweep = time.Minute

// sweepTimeout bounds one sweep, which touches only idle sessions and answers in milliseconds,
// so a stalled database holds the sweeper no longer than until the next. A round guess.
const sweepTimeout = 10 * time.Second

// jobSettings say how each instance runs summaries; live turns come first:
//   - Workers 1: a summary is a model call, and the model service runs one ask outside any session
//     at a time, so live turns keep its other places; more workers would only wait on it;
//   - Poll 1 s: a summary is wanted within seconds of the end (4 to 8 s measured), and one indexed
//     query a second costs the database nothing worth measuring;
//   - Timeout 4.5 min: a summary with data asks at most twice, each ask up to the model service's
//     2 min deadline on an ask outside any session, and 30 s more reads and saves around them; a
//     summary with data on Opus 5.5, on the developer's subscription, measured 8 to 12 s over
//     sessions of two and five turns, so the limit holds a long session's slower read;
//   - Lease 5 min: 30 s past the timeout, so a job still running is never taken by another;
//   - Attempts 5, FirstWait 10 s, four times longer each time (10 s, 40 s, 160 s, 640 s): about 14
//     minutes in all, through a model service restarting or a short outage, and the summary is only
//     needed by the user's next session; a round guess;
//   - KeepDone 7 days: long enough to look into a week of jobs, short enough that the table holds
//     only what is in flight and recent; failed jobs stay. A round guess;
//   - BusyWait 5 s, so 5 to 10 s with jitter: about a summary's own time (3 to 5 s measured), so
//     the place the model service keeps for background asks has likely freed. A round guess.
var jobSettings = jobs.Settings{
	Workers: 1, Poll: time.Second, Timeout: 270 * time.Second, Lease: 5 * time.Minute,
	Attempts: 5, FirstWait: 10 * time.Second, KeepDone: 7 * 24 * time.Hour, BusyWait: 5 * time.Second,
}

// deliverySettings say how each instance runs deliveries to products' servers, on a runner of
// their own, so a slow product's server never holds up a summary; the rest are as jobSettings but
// Timeout 30 s, twice a delivery's 15 s deadline, and Lease 1 min, 30 s past it:
//   - Workers 4: a delivery waits on another server, not on this machine, so several run at once;
//     four let three slow products' servers hold one each for their 15 s while a fourth still
//     delivers, for one idle poll a second each. A round guess, measured once products receive
//     sessions;
//   - Attempts 8: with the waits growing four times from 10 s, the last comes about 15 hours after
//     the first, through an outage of the product's server lasting most of a day, or a product
//     refusing deliveries until it holds a rotated secret, where the 5 a summary gets would give up
//     after about 14 minutes. A round guess, revisited once products' outages are seen.
var deliverySettings = jobs.Settings{
	Workers: 4, Poll: time.Second, Timeout: 30 * time.Second, Lease: time.Minute,
	Attempts: 8, FirstWait: 10 * time.Second, KeepDone: 7 * 24 * time.Hour, BusyWait: 5 * time.Second,
}

// pruneSweep is how often each instance deletes old jobs done: hourly, since a job done is kept for
// days and the delete reads an index of jobs done. A round guess.
const pruneSweep = time.Hour

// defaultModel answers turns unless VOICE_MODEL names another: Sonnet 5.5 reached its first words
// sooner than Haiku 5.5 when both were measured through the subscription route.
const defaultModel = "claude-sonnet-5-5"

// defaultModelURL is the model service's own default port on this machine, so the two meet
// without either being set. It names the address the service listens on rather than localhost,
// which is tried as ::1 first, where another process could answer.
const defaultModelURL = "http://127.0.0.1:8091"

// defaultTTSURL is Kokoro-FastAPI's own port on this machine, where compose publishes it.
const defaultTTSURL = "http://127.0.0.1:8880"

// The speech-to-text service's port on this machine, where compose publishes it, which serves
// both the realtime transcription API and the end-of-turn check; by address, as above.
const (
	defaultSTTURL       = "ws://127.0.0.1:8092"
	defaultTurnCheckURL = "http://127.0.0.1:8092"
)

// defaultSpeed is a voice's own pace, which Kokoro's speed field names 1.
const defaultSpeed = 1.0

// defaultVoice speaks replies of sessions whose start and agent name no voice: af_heart, the
// voice Kokoro's timings were measured in.
const defaultVoice = "af_heart"

// defaultSpeechThreshold is the probability from which a frame counts as speech: 0.5, the model's
// own default threshold. A starting value, tuned from measured sessions.
const defaultSpeechThreshold = 0.5

// defaultPause is how long the user is quiet before speech asks whether they have finished: 0.2 s,
// about the gap people leave before taking their turn in conversation, so the end-of-turn check,
// which judges pauses this short, sets how soon the agent answers rather than the wait. A starting
// value, tuned from measured turns.
const defaultPause = 200 * time.Millisecond

// defaultMaxWait is how long the user may stay quiet before their turn is taken anyway: 3 s. About
// 3 in 10 pauses within speech run past a second, people stopping to think, so a shorter wait cuts
// them off, and a longer one leaves a user who has finished waiting when the check misjudges. A
// starting value, tuned from measured turns.
const defaultMaxWait = 3 * time.Second

// defaultInterruptSpeech is how much of the user's speech over the agent, counted in frames judged
// speech, cuts in: 0.2 s, where two widely used voice agent frameworks start, so a single "Wait.",
// about 0.3 to 0.4 s, stops the agent, and a click or a breath shorter than it doesn't. People
// talking to an agent skip the "mm-hm"s they give a person, so nothing short needs letting
// through. A starting value, tuned from measured sessions.
const defaultInterruptSpeech = 200 * time.Millisecond

// defaultMaxSpeech is the most audio one turn's speech may run to before it is taken with what was
// heard: a minute, a round guess matching the most audio the speech-to-text service holds in one
// utterance, so none it is sent overflows. It stops speech that never pauses, such as a television
// left on, from running on, and keeps a turn's words well under what a turn may hold (a minute of
// speech is about 900 characters). Measured against real answers' lengths once sessions are timed.
const defaultMaxSpeech = time.Minute

// mostSpeech is the most VOICE_MAX_SPEECH may be: the minute of audio the speech-to-text service
// holds in one utterance. A turn whose speech ran on past it would have the service drop the
// utterance, and the turn would be lost instead of taken.
const mostSpeech = time.Minute

// defaultClientDir is where the drop-in client's build writes it, from the repository's root, where
// scripts/run.sh starts voice.
const defaultClientDir = "web/dist-client"

// startupCheck bounds the database read before serving. A database on this machine or in the
// same region answers in milliseconds; the driver has no connect timeout of its own, so without
// one an unreachable host hangs the start instead of failing it. A round guess.
const startupCheck = 10 * time.Second

func run(ctx context.Context, getenv func(string) string, log *slog.Logger) error {
	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	catalog := agents.NewCatalog(agentsstore.New(pool))
	// Read once before serving, so an unreachable database stops the start instead of the first request.
	checkCtx, cancelCheck := context.WithTimeout(ctx, startupCheck)
	offered, err := catalog.Available(checkCtx, cfg.workspace)
	cancelCheck()
	if err != nil {
		return fmt.Errorf("read the workspace's agents: %w", err)
	}

	userContext := usercontext.NewService(usercontextstore.New(pool))
	sessions, turns := conversationstore.NewSessions(pool), conversationstore.NewTurns(pool)
	tts, voices := newTextToSpeech(cfg)
	svc := newConversation(catalog, sessions, turns, modelhttp.New(cfg.modelURL), userContext, cfg, voices)
	deliveries := conversation.NewDeliveries(sessions, turns, conversationstore.NewWebhooks(pool), userContext, webhookhttp.New(webhookhttp.Deadline))
	transport, err := webrtctransport.New(cfg.audio, speech.MessageLimit(svc.TurnLimit()), log)
	if err != nil {
		return fmt.Errorf("audio transport: %w", err)
	}
	// Deferred before speech's own close, so it runs after: the port goes once every connection on it
	// has ended.
	defer transport.Close()
	var detection speech.SpeechDetection = detectionfake.Detection{}
	if cfg.detection == "silero" {
		if detection, err = silero.New(); err != nil {
			return fmt.Errorf("speech detection: %w", err)
		}
	}
	runners, err := jobRunners(jobstore.New(pool), jobSettings, deliverySettings, svc, deliveries, log)
	if err != nil {
		return err
	}
	// Ends idle sessions, runs background jobs and deletes old ones until voice stops, each waited
	// for, so none runs past it; each job in hand is let go to run again.
	sweepCtx, stopSweep := context.WithCancel(ctx)
	var sweeper sync.WaitGroup
	sweeper.Go(func() { sweep(sweepCtx, sessionSweep, "idle sessions ended", svc.EndIdle, log) })
	for _, r := range runners {
		sweeper.Go(func() { r.Run(sweepCtx) })
	}
	for _, r := range runners { // each deletes its own kinds' jobs done
		sweeper.Go(func() { sweep(sweepCtx, pruneSweep, "old jobs deleted", r.Prune, log) })
	}
	defer sweeper.Wait()
	defer stopSweep()
	audio := speech.NewService(svc, transport, tts, detection, cfg.stt, cfg.endOfTurn, speech.Settings{MaxConnections: cfg.maxAudio, TextToSpeech: cfg.tts, Listening: cfg.listening}, log)
	// Waits, once the server has stopped taking requests, for every connection to end, so none is
	// set up after it.
	defer audio.Close()
	routes := api.NewRouter(cfg.key, productkey.NewKeys(productkeystore.New(pool)), time.Now, log)
	conversationhttp.Register(routes, svc)
	speechhttp.Register(routes, audio)
	routes.ServeClient(cfg.clientDir)
	// The server's own timeouts are round guesses that only bound misbehaving clients. A
	// request's headers and body arrive in milliseconds, so 5s and 15s stop a client that opens
	// connections and sends slowly; each route replaces the write timeout with its own deadline;
	// a browser keeps its connection for the next turn, and 2 minutes covers a pause between
	// typed turns without holding idle sockets for long.
	srv := &http.Server{
		Handler:           routes,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	// Audio connections stop with the HTTP server, not after its grace period, so none takes a
	// turn while the server stops; the deferred Close above waits for them to finish.
	srv.RegisterOnShutdown(audio.Close)
	// Bind before announcing, so a port already in use fails here, not after "listening".
	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.addr, err)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("voice listening", "addr", ln.Addr().String(), "agents", len(offered))
	if err := serve(ctx, srv, ln, shutdownGrace); err != nil {
		return err
	}
	log.Info("voice stopped cleanly")
	return nil
}

// newTextToSpeech returns what speaks replies and, from the same service, the list of voices a
// session may be started with: speech's core never asks for the list, so it is the adapter's own
// method, handed to conversation as its port.
func newTextToSpeech(cfg config) (speech.TextToSpeech, conversation.VoiceList) {
	if cfg.tts == "fake" {
		return ttsfake.TextToSpeech{}, ttsfake.TextToSpeech{}
	}
	k := kokoro.New(cfg.ttsURL, kokoro.Options{FirstAudio: cfg.firstAudio})
	return k, k
}

// newConversation returns the conversation service, checking the voice a start names against
// voices. Kept apart from run so a test can build it as run does: a service built without the
// list refuses every start that names a voice.
func newConversation(catalog *agents.Catalog, sessions conversation.Sessions, turns conversation.Turns, model conversation.Model, userContext *usercontext.Service, cfg config, voices conversation.VoiceList) *conversation.Service {
	return conversation.NewService(catalog, sessions, turns, model, userContext, cfg.settings, conversation.WithVoices(voices))
}

// summaryJob runs a session's summary job. When the model service has no free place for it, the
// job waits and tries again, and the wait does not use up one of its runs.
func summaryJob(svc *conversation.Service) func(context.Context, uuid.UUID, uuid.UUID, conversation.SummaryArgs) error {
	return func(ctx context.Context, workspace, user uuid.UUID, args conversation.SummaryArgs) error {
		err := svc.Summarize(ctx, workspace, user, args)
		if errors.Is(err, conversation.ErrModelBusy) {
			return jobs.Busy(err)
		}
		return err
	}
}

// jobRunners returns this instance's job runners over store, each claiming only the kinds it
// has handlers for: one for summaries and settling their status, run by summaries' settings, which wait on the model
// service's one place for background asks, and one for deliveries, run by deliveries', which wait
// on products' servers, so neither waits behind the other.
func jobRunners(store jobs.Store, summaries, deliveries jobs.Settings, svc *conversation.Service, d *conversation.Deliveries, log *slog.Logger) ([]*jobs.Runner, error) {
	summary, err := jobs.NewRunner(store, summaries, log)
	if err != nil {
		return nil, fmt.Errorf("the summaries' runner: %w", err)
	}
	jobs.Handle(summary, conversation.SummaryJob, summaryJob(svc))
	jobs.Handle(summary, conversation.SummarySettleJob, svc.SettleSummary)
	delivery, err := jobs.NewRunner(store, deliveries, log)
	if err != nil {
		return nil, fmt.Errorf("the deliveries' runner: %w", err)
	}
	jobs.Handle(delivery, conversation.DeliveryJob, deliveryJob(d))
	return []*jobs.Runner{summary, delivery}, nil
}

// deliveryJob runs a session's delivery to its workspace's webhook: one whose
// webhook is stopped, or whose URL no longer meets the rule, fails at once,
// logged with its session, since posting it again would change nothing until
// the URL is set again.
func deliveryJob(deliveries *conversation.Deliveries) func(context.Context, uuid.UUID, uuid.UUID, conversation.DeliveryArgs) error {
	return func(ctx context.Context, workspace, user uuid.UUID, args conversation.DeliveryArgs) error {
		err := deliveries.Deliver(ctx, workspace, user, args)
		if errors.Is(err, conversation.ErrWebhookStopped) || errors.Is(err, conversation.ErrWebhookURL) {
			return jobs.Permanent(err)
		}
		return err
	}
}

// sweep runs do at once, then every interval until ctx is done, logging how
// many rows it changed as done. A failed sweep is logged and the next tries
// again.
func sweep(ctx context.Context, every time.Duration, done string, do func(context.Context) (int, error), log *slog.Logger) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for first := true; ; first = false {
		if !first {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
		once, cancel := context.WithTimeout(ctx, sweepTimeout)
		n, err := do(once)
		cancel()
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("sweep failed", "sweep", done, "err", err)
		case n > 0:
			log.Info(done, "count", n)
		}
	}
}

// shutdownGrace bounds the wait, once voice stops, for the requests it
// cancelled to finish: a cancelled turn takes a few seconds at most to save
// how it ended.
const shutdownGrace = 10 * time.Second

// serve answers requests on ln until ctx is done, then stops: it takes no new
// requests, cancels those in flight, and waits up to grace for them to finish.
// A turn can stream for far longer than a stopping service is given, so it is
// ended, not waited for: saved lost, the way a turn whose caller went away is,
// and its caller told.
func serve(ctx context.Context, srv *http.Server, ln net.Listener, grace time.Duration) error {
	requests, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	srv.BaseContext = func(net.Listener) context.Context { return requests }
	srv.RegisterOnShutdown(cancelRequests) // run once the listener is closed
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	select {
	case err := <-served:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return fmt.Errorf("shut down: %w", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
