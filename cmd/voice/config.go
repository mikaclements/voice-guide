package main

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/speech"
	"github.com/mikaclements/voice-guide/internal/speech/endofturn/smartturn"
	"github.com/mikaclements/voice-guide/internal/speech/stt/realtime"
	webrtctransport "github.com/mikaclements/voice-guide/internal/speech/transport/webrtc"
	"github.com/mikaclements/voice-guide/internal/speech/tts/kokoro"
	"github.com/mikaclements/voice-guide/internal/ticket"
)

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
	// stt and endOfTurn are the clients for those addresses, built once where the addresses are read.
	stt       *realtime.SpeechToText
	endOfTurn *smartturn.EndOfTurn
	// clientDir is the folder voice serves the drop-in client from.
	clientDir string
	// settings bound, in characters, what a session loads from user context, what a product
	// sends as session context and as screen input, and the words of a turn; bound how long a
	// turn runs; and name the model that answers turns.
	settings conversation.Settings
}

// defaultContextLimit is about 5,000 tokens of English: room for a two-page resume (about
// 5,000 characters) as session context, and for past session summaries in user context;

// loadConfig reads every setting from the environment, one loader per area, in the order the
// areas are named here. The first setting out of bounds stops the start.
func loadConfig(getenv func(string) string) (config, error) {
	var cfg config
	for _, load := range []func(*config, func(string) string) error{
		(*config).loadConversation,
		(*config).loadModel,
		(*config).loadTextToSpeech,
		(*config).loadSpeechToText,
		(*config).loadAudio,
		(*config).loadListening,
		(*config).loadServer,
		(*config).loadDatabase,
		(*config).loadAccess,
	} {
		if err := load(&cfg, getenv); err != nil {
			return config{}, err
		}
	}
	return cfg, nil
}

// loadConversation reads how long a turn runs and a session idles, and the limits on what a
// session loads and a product sends.
func (cfg *config) loadConversation(getenv func(string) string) (err error) {
	if cfg.settings.TurnDeadline, err = positiveDuration(getenv, "VOICE_TURN_TIMEOUT", defaultTurnTimeout); err != nil {
		return err
	}
	if cfg.settings.SessionIdle, err = positiveDuration(getenv, "VOICE_SESSION_IDLE", defaultSessionIdle); err != nil {
		return err
	}
	// Idle counts from a turn's start while it runs, so a session idle sooner than a turn may run
	// could end with its user waiting on a reply.
	if cfg.settings.SessionIdle <= cfg.settings.TurnDeadline {
		return fmt.Errorf("VOICE_SESSION_IDLE: %s is no longer than a turn may run, %s", cfg.settings.SessionIdle, cfg.settings.TurnDeadline)
	}
	if cfg.settings.UserContextChars, err = positive(getenv, "VOICE_USER_CONTEXT_MAX_CHARS", defaultContextLimit); err != nil {
		return err
	}
	if cfg.settings.SessionContextChars, err = positive(getenv, "VOICE_SESSION_CONTEXT_MAX_CHARS", defaultContextLimit); err != nil {
		return err
	}
	if cfg.settings.ScreenInputChars, err = positive(getenv, "VOICE_SCREEN_INPUT_MAX_CHARS", defaultScreenInputLimit); err != nil {
		return err
	}
	cfg.settings.WordsChars, err = positive(getenv, "VOICE_WORDS_MAX_CHARS", defaultWordsLimit)
	return err
}

// loadModel reads the model that answers turns and where its service listens.
func (cfg *config) loadModel(getenv func(string) string) error {
	cfg.settings.Model = cmp.Or(getenv("VOICE_MODEL"), defaultModel)
	cfg.modelURL = cmp.Or(getenv("VOICE_MODEL_URL"), defaultModelURL)
	return httpAddress("VOICE_MODEL_URL", cfg.modelURL)
}

// loadTextToSpeech reads what speaks replies and how.
func (cfg *config) loadTextToSpeech(getenv func(string) string) (err error) {
	switch cfg.tts = cmp.Or(getenv("VOICE_TTS"), "kokoro"); cfg.tts {
	case "kokoro", "fake":
	default:
		return fmt.Errorf("VOICE_TTS: %q is not kokoro or fake", cfg.tts)
	}
	cfg.ttsURL, cfg.voice = cmp.Or(getenv("VOICE_TTS_URL"), defaultTTSURL), cmp.Or(getenv("VOICE_TTS_VOICE"), defaultVoice)
	if err = httpAddress("VOICE_TTS_URL", cfg.ttsURL); err != nil {
		return err
	}
	cfg.firstAudio, err = positiveDuration(getenv, "VOICE_TTS_FIRST_AUDIO", kokoro.DefaultFirstAudio)
	return err
}

// loadSpeechToText reads where the speech-to-text service listens, and builds its two clients
// then, so a bad address stops the start before anything is wired.
func (cfg *config) loadSpeechToText(getenv func(string) string) (err error) {
	cfg.sttURL, cfg.turnCheckURL = cmp.Or(getenv("VOICE_STT_URL"), defaultSTTURL), cmp.Or(getenv("VOICE_TURN_CHECK_URL"), defaultTurnCheckURL)
	if cfg.stt, err = realtime.New(cfg.sttURL, realtime.Options{}); err != nil {
		return fmt.Errorf("VOICE_STT_URL: %w", err)
	}
	if cfg.endOfTurn, err = smartturn.New(cfg.turnCheckURL, 0); err != nil {
		return fmt.Errorf("VOICE_TURN_CHECK_URL: %w", err)
	}
	return nil
}

// loadAudio reads where browsers' audio arrives and how many connections it may hold.
func (cfg *config) loadAudio(getenv func(string) string) (err error) {
	if cfg.audio.IPs, err = ipList(getenv, "VOICE_AUDIO_IPS"); err != nil {
		return err
	}
	if v := getenv("VOICE_AUDIO_UDP_PORT"); v != "" {
		if cfg.audio.Port, err = strconv.Atoi(v); err != nil || cfg.audio.Port < 1 || cfg.audio.Port > 65535 {
			return fmt.Errorf("VOICE_AUDIO_UDP_PORT: %q is not a port from 1 to 65535", v)
		}
	}
	if cfg.audio.Announce, err = ipList(getenv, "VOICE_AUDIO_ANNOUNCE_IPS"); err != nil {
		return err
	}
	// An announced address stands for every address of its family that takes audio, and an answer
	// keeps one candidate per address and port, so on a shared port only one of them is reached: the
	// one the forwarded audio arrives on must be the one named.
	if len(cfg.audio.Announce) > 0 && len(cfg.audio.IPs) == 0 {
		return errors.New("VOICE_AUDIO_ANNOUNCE_IPS: name the addresses of this machine it stands for in VOICE_AUDIO_IPS")
	}
	cfg.maxAudio, err = positive(getenv, "VOICE_MAX_AUDIO_CONNECTIONS", defaultMaxAudio)
	return err
}

// loadListening reads what notices speech, and how it tells speaking, pausing and cutting in.
func (cfg *config) loadListening(getenv func(string) string) (err error) {
	switch cfg.detection = cmp.Or(getenv("VOICE_SPEECH_DETECTION"), "silero"); cfg.detection {
	case "silero", "fake":
	default:
		return fmt.Errorf("VOICE_SPEECH_DETECTION: %q is not silero or fake", cfg.detection)
	}
	cfg.listening.Threshold = defaultSpeechThreshold
	if v := getenv("VOICE_SPEECH_THRESHOLD"); v != "" {
		p, err := strconv.ParseFloat(v, 32)
		if err != nil || math.IsNaN(p) || p <= 0 || p > 1 {
			return fmt.Errorf("VOICE_SPEECH_THRESHOLD: %q is not a probability above 0, such as 0.5", v)
		}
		cfg.listening.Threshold = float32(p)
	}
	if cfg.listening.Pause, err = positiveDuration(getenv, "VOICE_PAUSE", defaultPause); err != nil {
		return err
	}
	if cfg.listening.MaxWait, err = positiveDuration(getenv, "VOICE_MAX_WAIT", defaultMaxWait); err != nil {
		return err
	}
	if cfg.listening.MaxWait <= cfg.listening.Pause {
		return fmt.Errorf("VOICE_MAX_WAIT: %s is no longer than the pause, %s, which comes first", cfg.listening.MaxWait, cfg.listening.Pause)
	}
	// Audio that stops coming counts as at most speech.MaxMissing of silence, so a longer wait
	// would never be up while the user's audio is lost.
	if cfg.listening.MaxWait > speech.MaxMissing {
		return fmt.Errorf("VOICE_MAX_WAIT: %s is longer than %s, the most silence lost audio counts as", cfg.listening.MaxWait, speech.MaxMissing)
	}
	if cfg.listening.InterruptSpeech, err = positiveDuration(getenv, "VOICE_INTERRUPT_SPEECH", defaultInterruptSpeech); err != nil {
		return err
	}
	if cfg.listening.MaxSpeech, err = positiveDuration(getenv, "VOICE_MAX_SPEECH", defaultMaxSpeech); err != nil {
		return err
	}
	// Speech over the agent cuts in once its frames judged speech add up to the interrupt speech, so
	// a turn's speech must run longer for continuous speech to be held before it reaches its most.
	// Scattered frames a pause apart, which take longer to add up, may reach the most first and are
	// passed over: they are noise.
	if cfg.listening.MaxSpeech <= cfg.listening.InterruptSpeech {
		return fmt.Errorf("VOICE_MAX_SPEECH: %s is no longer than the interrupt speech, %s, so even continuous speech over the agent would reach it before it could cut in", cfg.listening.MaxSpeech, cfg.listening.InterruptSpeech)
	}
	if cfg.listening.MaxSpeech > mostSpeech {
		return fmt.Errorf("VOICE_MAX_SPEECH: %s is longer than %s, the most audio the speech-to-text service holds in one utterance", cfg.listening.MaxSpeech, mostSpeech)
	}
	return nil
}

// loadServer reads where voice listens and the folder it serves the drop-in client from.
func (cfg *config) loadServer(getenv func(string) string) error {
	// Only this machine by default; a container sets an address on all interfaces.
	cfg.addr = cmp.Or(getenv("VOICE_ADDR"), "127.0.0.1:8080")
	// The default folder may not be built yet, in a fresh clone or for a check with no page, and is
	// answered 404 until it is; a folder named on purpose must exist, so a mistyped one stops the start.
	if cfg.clientDir = getenv("VOICE_CLIENT_DIR"); cfg.clientDir == "" {
		cfg.clientDir = defaultClientDir
	} else if info, err := os.Stat(cfg.clientDir); err != nil {
		return fmt.Errorf("VOICE_CLIENT_DIR: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("VOICE_CLIENT_DIR: %q is not a folder", cfg.clientDir)
	}
	return nil
}

// loadDatabase reads where the database is.
func (cfg *config) loadDatabase(getenv func(string) string) error {
	if cfg.databaseURL = getenv("DATABASE_URL"); cfg.databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	return nil
}

// loadAccess reads the workspace voice serves and the key that signs its tickets.
func (cfg *config) loadAccess(getenv func(string) string) (err error) {
	if cfg.workspace, err = uuid.Parse(getenv("VOICE_WORKSPACE_ID")); err != nil {
		return fmt.Errorf("VOICE_WORKSPACE_ID: %w", err)
	}
	if cfg.key, err = ticket.NewKey([]byte(getenv("VOICE_TICKET_KEY"))); err != nil {
		return fmt.Errorf("VOICE_TICKET_KEY: %w", err)
	}
	return nil
}

// httpAddress is nil when value, the setting name's, is an http or https address.
func httpAddress(name, value string) error {
	if u, err := url.Parse(value); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s: %q is not an http or https address", name, value)
	}
	return nil
}

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
