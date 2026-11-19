// Command voice runs the voice service: it wires the conversation, speech, usercontext
// and agents modules together. Only programs in cmd/ wire modules.
// Settings come from the environment; scripts/run.sh sets them for local development.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/agents/store/postgres"
	"github.com/mikaclements/voice-guide/internal/api"
	"github.com/mikaclements/voice-guide/internal/conversation"
	conversationhttp "github.com/mikaclements/voice-guide/internal/conversation/http"
	modelhttp "github.com/mikaclements/voice-guide/internal/conversation/model/http"
	conversationstore "github.com/mikaclements/voice-guide/internal/conversation/store/postgres"
	webhookhttp "github.com/mikaclements/voice-guide/internal/conversation/webhook/http"
	"github.com/mikaclements/voice-guide/internal/jobs"
	jobstore "github.com/mikaclements/voice-guide/internal/jobs/store/postgres"
	"github.com/mikaclements/voice-guide/internal/productkey"
	productkeys "github.com/mikaclements/voice-guide/internal/productkey/store/postgres"
	"github.com/mikaclements/voice-guide/internal/speech"
	detectionfake "github.com/mikaclements/voice-guide/internal/speech/detection/fake"
	"github.com/mikaclements/voice-guide/internal/speech/detection/silero"
	"github.com/mikaclements/voice-guide/internal/speech/endofturn/smartturn"
	speechhttp "github.com/mikaclements/voice-guide/internal/speech/http"
	"github.com/mikaclements/voice-guide/internal/speech/stt/realtime"
	webrtctransport "github.com/mikaclements/voice-guide/internal/speech/transport/webrtc"
	ttsfake "github.com/mikaclements/voice-guide/internal/speech/tts/fake"
	"github.com/mikaclements/voice-guide/internal/speech/tts/kokoro"
	"github.com/mikaclements/voice-guide/internal/ticket"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	contextitems "github.com/mikaclements/voice-guide/internal/usercontext/store/postgres"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(context.Background(), os.Getenv, log); err != nil {
		log.Error("voice stopped", "err", err)
		os.Exit(1)
	}
}

type config struct {
	addr        string
	databaseURL string
	workspace   uuid.UUID
	key         ticket.Key
	// modelURL is where the model service listens.
	modelURL string
	// audio says where browsers' audio arrives: the addresses of this machine that take it (none
	// means its private and loopback ones), the one UDP port every connection shares (none gives
	// each its own), and the addresses written into answers in place of the machine's own.
	audio webrtctransport.Addresses
	// maxAudio bounds the audio connections this server holds at once.
	maxAudio int
	// tts names what speaks replies, kokoro or fake; ttsURL is where Kokoro-FastAPI listens; voice
	// is the voice replies are spoken in; firstAudio bounds the wait for a phrase's first audio.
	tts        string
	ttsURL     string
	voice      string
	firstAudio time.Duration
	// detection names what notices speech, silero or fake; listening sets how speech notices the
	// user speaking, pausing and cutting in.
	detection string
	listening speech.Listening
	// sttURL is where the speech-to-text service takes the realtime transcription API, and
	// turnCheckURL where its end-of-turn check listens.
	sttURL       string
	turnCheckURL string
	// clientDir is the folder voice serves the drop-in client from.
	clientDir string
	// settings bound, in characters, what a session loads from user context, what a product
	// sends as session context and as screen input, and the words of a turn; bound how long a
	// turn runs; and name the model that answers turns.
	settings conversation.Settings
}

// defaultContextLimit is about 5,000 tokens of English: room for a two-page resume (about
// 5,000 characters) as session context, and for past session summaries in user context;
// tuned once turns are timed. Both limits start here.
const defaultContextLimit = 20_000

// defaultScreenInputLimit is about 2,000 tokens of English with each turn: a page of what the
// user is looking at, without making every turn slow to read.
const defaultScreenInputLimit = 8_000

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
//   - Timeout 30 s: twice the model service's 15 s deadline on a reply, so a run whose reply ran
//     to it still reads and saves around it;
//   - Lease 1 min: 30 s past the timeout, so a job still running is never taken by another;
//   - Attempts 5, FirstWait 10 s, four times longer each time (10 s, 40 s, 160 s, 640 s): about 14
//     minutes in all, through a model service restarting or a short outage, and the summary is only
//     needed by the user's next session; a round guess;
//   - KeepDone 7 days: long enough to look into a week of jobs, short enough that the table holds
//     only what is in flight and recent; failed jobs stay. A round guess;
//   - BusyWait 5 s, so 5 to 10 s with jitter: about a summary's own time (3 to 5 s measured), so
//     the place the model service keeps for background asks has likely freed. A round guess.
var jobSettings = jobs.Settings{
	Workers: 1, Poll: time.Second, Timeout: 30 * time.Second, Lease: time.Minute,
	Attempts: 5, FirstWait: 10 * time.Second, KeepDone: 7 * 24 * time.Hour, BusyWait: 5 * time.Second,
}

// deliverySettings say how each instance runs deliveries to products' servers, on a runner of
// their own, so a slow product's server never holds up a summary; the rest are as jobSettings, a
// run's 30 s twice a delivery's 15 s deadline:
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

// defaultVoice speaks replies until agents choose their own: af_heart, the voice Kokoro's
// timings were measured in.
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

func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{addr: getenv("VOICE_ADDR"), databaseURL: getenv("DATABASE_URL")}
	var err error
	if cfg.settings.TurnDeadline, err = positiveDuration(getenv, "VOICE_TURN_TIMEOUT", defaultTurnTimeout); err != nil {
		return config{}, err
	}
	if cfg.settings.SessionIdle, err = positiveDuration(getenv, "VOICE_SESSION_IDLE", defaultSessionIdle); err != nil {
		return config{}, err
	}
	// Idle counts from a turn's start while it runs, so a session idle sooner than a turn may run
	// could end with its user waiting on a reply.
	if cfg.settings.SessionIdle <= cfg.settings.TurnDeadline {
		return config{}, fmt.Errorf("VOICE_SESSION_IDLE: %s is no longer than a turn may run, %s", cfg.settings.SessionIdle, cfg.settings.TurnDeadline)
	}
	if cfg.settings.Model = getenv("VOICE_MODEL"); cfg.settings.Model == "" {
		cfg.settings.Model = defaultModel
	}
	if cfg.modelURL = getenv("VOICE_MODEL_URL"); cfg.modelURL == "" {
		cfg.modelURL = defaultModelURL
	}
	if u, err := url.Parse(cfg.modelURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return config{}, fmt.Errorf("VOICE_MODEL_URL: %q is not an http or https address", cfg.modelURL)
	}
	switch cfg.tts = cmp.Or(getenv("VOICE_TTS"), "kokoro"); cfg.tts {
	case "kokoro", "fake":
	default:
		return config{}, fmt.Errorf("VOICE_TTS: %q is not kokoro or fake", cfg.tts)
	}
	cfg.ttsURL, cfg.voice = cmp.Or(getenv("VOICE_TTS_URL"), defaultTTSURL), cmp.Or(getenv("VOICE_TTS_VOICE"), defaultVoice)
	if u, err := url.Parse(cfg.ttsURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return config{}, fmt.Errorf("VOICE_TTS_URL: %q is not an http or https address", cfg.ttsURL)
	}
	if cfg.firstAudio, err = positiveDuration(getenv, "VOICE_TTS_FIRST_AUDIO", kokoro.DefaultFirstAudio); err != nil {
		return config{}, err
	}
	cfg.sttURL, cfg.turnCheckURL = cmp.Or(getenv("VOICE_STT_URL"), defaultSTTURL), cmp.Or(getenv("VOICE_TURN_CHECK_URL"), defaultTurnCheckURL)
	if _, err := realtime.New(cfg.sttURL, realtime.Options{}); err != nil {
		return config{}, fmt.Errorf("VOICE_STT_URL: %w", err)
	}
	if _, err := smartturn.New(cfg.turnCheckURL, 0); err != nil {
		return config{}, fmt.Errorf("VOICE_TURN_CHECK_URL: %w", err)
	}
	if cfg.audio.IPs, err = ipList(getenv, "VOICE_AUDIO_IPS"); err != nil {
		return config{}, err
	}
	if v := getenv("VOICE_AUDIO_UDP_PORT"); v != "" {
		if cfg.audio.Port, err = strconv.Atoi(v); err != nil || cfg.audio.Port < 1 || cfg.audio.Port > 65535 {
			return config{}, fmt.Errorf("VOICE_AUDIO_UDP_PORT: %q is not a port from 1 to 65535", v)
		}
	}
	if cfg.audio.Announce, err = ipList(getenv, "VOICE_AUDIO_ANNOUNCE_IPS"); err != nil {
		return config{}, err
	}
	// An announced address stands for every address of its family that takes audio, and an answer
	// keeps one candidate per address and port, so on a shared port only one of them is reached: the
	// one the forwarded audio arrives on must be the one named.
	if len(cfg.audio.Announce) > 0 && len(cfg.audio.IPs) == 0 {
		return config{}, errors.New("VOICE_AUDIO_ANNOUNCE_IPS: name the addresses of this machine it stands for in VOICE_AUDIO_IPS")
	}
	if cfg.addr == "" {
		// Only this machine by default; a container sets an address on all interfaces.
		cfg.addr = "127.0.0.1:8080"
	}
	// The default folder may not be built yet, in a fresh clone or for a check with no page, and is
	// answered 404 until it is; a folder named on purpose must exist, so a mistyped one stops the start.
	if cfg.clientDir = getenv("VOICE_CLIENT_DIR"); cfg.clientDir == "" {
		cfg.clientDir = defaultClientDir
	} else if info, err := os.Stat(cfg.clientDir); err != nil {
		return config{}, fmt.Errorf("VOICE_CLIENT_DIR: %w", err)
	} else if !info.IsDir() {
		return config{}, fmt.Errorf("VOICE_CLIENT_DIR: %q is not a folder", cfg.clientDir)
	}
	if cfg.databaseURL == "" {
		return config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.workspace, err = uuid.Parse(getenv("VOICE_WORKSPACE_ID")); err != nil {
		return config{}, fmt.Errorf("VOICE_WORKSPACE_ID: %w", err)
	}
	if cfg.key, err = ticket.NewKey([]byte(getenv("VOICE_TICKET_KEY"))); err != nil {
		return config{}, fmt.Errorf("VOICE_TICKET_KEY: %w", err)
	}
	if cfg.settings.UserContextChars, err = positive(getenv, "VOICE_USER_CONTEXT_MAX_CHARS", defaultContextLimit); err != nil {
		return config{}, err
	}
	if cfg.settings.SessionContextChars, err = positive(getenv, "VOICE_SESSION_CONTEXT_MAX_CHARS", defaultContextLimit); err != nil {
		return config{}, err
	}
	if cfg.settings.ScreenInputChars, err = positive(getenv, "VOICE_SCREEN_INPUT_MAX_CHARS", defaultScreenInputLimit); err != nil {
		return config{}, err
	}
	if cfg.settings.WordsChars, err = positive(getenv, "VOICE_WORDS_MAX_CHARS", defaultWordsLimit); err != nil {
		return config{}, err
	}
	if cfg.maxAudio, err = positive(getenv, "VOICE_MAX_AUDIO_CONNECTIONS", defaultMaxAudio); err != nil {
		return config{}, err
	}
	switch cfg.detection = cmp.Or(getenv("VOICE_SPEECH_DETECTION"), "silero"); cfg.detection {
	case "silero", "fake":
	default:
		return config{}, fmt.Errorf("VOICE_SPEECH_DETECTION: %q is not silero or fake", cfg.detection)
	}
	cfg.listening.Threshold = defaultSpeechThreshold
	if v := getenv("VOICE_SPEECH_THRESHOLD"); v != "" {
		p, err := strconv.ParseFloat(v, 32)
		if err != nil || math.IsNaN(p) || p <= 0 || p > 1 {
			return config{}, fmt.Errorf("VOICE_SPEECH_THRESHOLD: %q is not a probability above 0, such as 0.5", v)
		}
		cfg.listening.Threshold = float32(p)
	}
	if cfg.listening.Pause, err = positiveDuration(getenv, "VOICE_PAUSE", defaultPause); err != nil {
		return config{}, err
	}
	if cfg.listening.MaxWait, err = positiveDuration(getenv, "VOICE_MAX_WAIT", defaultMaxWait); err != nil {
		return config{}, err
	}
	if cfg.listening.MaxWait <= cfg.listening.Pause {
		return config{}, fmt.Errorf("VOICE_MAX_WAIT: %s is no longer than the pause, %s, which comes first", cfg.listening.MaxWait, cfg.listening.Pause)
	}
	// Audio that stops coming counts as at most speech.MaxMissing of silence, so a longer wait
	// would never be up while the user's audio is lost.
	if cfg.listening.MaxWait > speech.MaxMissing {
		return config{}, fmt.Errorf("VOICE_MAX_WAIT: %s is longer than %s, the most silence lost audio counts as", cfg.listening.MaxWait, speech.MaxMissing)
	}
	if cfg.listening.InterruptSpeech, err = positiveDuration(getenv, "VOICE_INTERRUPT_SPEECH", defaultInterruptSpeech); err != nil {
		return config{}, err
	}
	if cfg.listening.MaxSpeech, err = positiveDuration(getenv, "VOICE_MAX_SPEECH", defaultMaxSpeech); err != nil {
		return config{}, err
	}
	// Speech over the agent cuts in once its frames judged speech add up to the interrupt speech, so
	// a turn's speech must run longer for continuous speech to be held before it reaches its most.
	// Scattered frames a pause apart, which take longer to add up, may reach the most first and are
	// passed over: they are noise.
	if cfg.listening.MaxSpeech <= cfg.listening.InterruptSpeech {
		return config{}, fmt.Errorf("VOICE_MAX_SPEECH: %s is no longer than the interrupt speech, %s, so even continuous speech over the agent would reach it before it could cut in", cfg.listening.MaxSpeech, cfg.listening.InterruptSpeech)
	}
	if cfg.listening.MaxSpeech > mostSpeech {
		return config{}, fmt.Errorf("VOICE_MAX_SPEECH: %s is longer than %s, the most audio the speech-to-text service holds in one utterance", cfg.listening.MaxSpeech, mostSpeech)
	}
	return cfg, nil
}

// ipList reads the setting name as IP addresses separated by commas or spaces, or none when it is unset.
func ipList(getenv func(string) string, name string) ([]net.IP, error) {
	var ips []net.IP
	for _, v := range strings.FieldsFunc(getenv(name), func(r rune) bool { return r == ',' || r == ' ' }) {
		ip := net.ParseIP(v)
		if ip == nil {
			return nil, fmt.Errorf("%s: %q is not an IP address", name, v)
		}
		ips = append(ips, ip)
	}
	return ips, nil
}

// positiveDuration reads the setting name as a positive duration, or def when it is unset.
func positiveDuration(getenv func(string) string, name string, def time.Duration) (time.Duration, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s: %q is not a positive duration, such as %s", name, v, def)
	}
	return d, nil
}

// positive reads the setting name as a positive whole number, or def when it is unset.
func positive(getenv func(string) string, name string, def int) (int, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s: %q is not a positive whole number", name, v)
	}
	return n, nil
}

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
	catalog := agents.NewCatalog(postgres.New(pool))
	// Read once before serving, so an unreachable database stops the start instead of the first request.
	checkCtx, cancelCheck := context.WithTimeout(ctx, startupCheck)
	offered, err := catalog.Available(checkCtx, cfg.workspace)
	cancelCheck()
	if err != nil {
		return fmt.Errorf("read the workspace's agents: %w", err)
	}

	userContext := usercontext.NewService(contextitems.New(pool))
	sessions, turns := conversationstore.New(pool), conversationstore.NewTurns(pool)
	svc := conversation.NewService(catalog, sessions, turns, modelhttp.New(cfg.modelURL), userContext, cfg.settings)
	deliveries := conversation.NewDeliveries(sessions, turns, conversationstore.NewWebhooks(pool), userContext, webhookhttp.New(webhookhttp.Deadline))
	transport, err := webrtctransport.New(cfg.audio, speech.MessageLimit(svc.TurnLimit()), log)
	if err != nil {
		return fmt.Errorf("audio transport: %w", err)
	}
	// Deferred before speech's own close, so it runs after: the port goes once every connection on it
	// has ended.
	defer transport.Close()
	var tts speech.TextToSpeech = kokoro.New(cfg.ttsURL, kokoro.Options{FirstAudio: cfg.firstAudio})
	if cfg.tts == "fake" {
		tts = ttsfake.TextToSpeech{}
	}
	var detection speech.SpeechDetection = detectionfake.Detection{}
	if cfg.detection == "silero" {
		if detection, err = silero.New(); err != nil {
			return fmt.Errorf("speech detection: %w", err)
		}
	}
	stt, err := realtime.New(cfg.sttURL, realtime.Options{})
	if err != nil {
		return fmt.Errorf("speech-to-text: %w", err)
	}
	endOfTurn, err := smartturn.New(cfg.turnCheckURL, 0)
	if err != nil {
		return fmt.Errorf("end-of-turn check: %w", err)
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
	audio := speech.NewService(svc, transport, tts, detection, stt, endOfTurn, speech.Settings{MaxConnections: cfg.maxAudio, Voice: cfg.voice, TextToSpeech: cfg.tts, Listening: cfg.listening}, log)
	// Waits, once the server has stopped taking requests, for every connection to end, so none is
	// set up after it.
	defer audio.Close()
	routes := api.NewRouter(cfg.key, productkey.NewKeys(productkeys.New(pool)), time.Now, log)
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

// summaryJob runs a session's summary job: a summary the model service had
// no place for is busy, let go without using a run.
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
// has handlers for: one for summaries, run by summaries' settings, which wait on the model
// service's one place for background asks, and one for deliveries, run by deliveries', which wait
// on products' servers, so neither waits behind the other.
func jobRunners(store jobs.Store, summaries, deliveries jobs.Settings, svc *conversation.Service, d *conversation.Deliveries, log *slog.Logger) ([]*jobs.Runner, error) {
	summary, err := jobs.NewRunner(store, summaries, log)
	if err != nil {
		return nil, fmt.Errorf("the summaries' runner: %w", err)
	}
	jobs.Handle(summary, conversation.SummaryJob, summaryJob(svc))
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
