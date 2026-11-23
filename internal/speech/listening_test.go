package speech_test

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/conversation"
	sessionsfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/speech"
	detectionfake "github.com/mikaclements/voice-guide/internal/speech/detection/fake"
	endofturnfake "github.com/mikaclements/voice-guide/internal/speech/endofturn/fake"
	sttfake "github.com/mikaclements/voice-guide/internal/speech/stt/fake"
	"github.com/mikaclements/voice-guide/internal/speech/transport/fake"
)

// waitLogged waits until at least n records are logged, and returns them.
func waitLogged(t *testing.T, l *logged, n int, what string) []map[string]slog.Value {
	t.Helper()
	deadline := time.After(wait)
	for {
		l.mu.Lock()
		got := slices.Clone(l.records)
		if l.wrote == nil {
			l.wrote = make(chan struct{})
		}
		wrote := l.wrote
		l.mu.Unlock()
		if len(got) >= n {
			return got
		}
		select {
		case <-wrote:
		case <-deadline:
			t.Fatalf("waited %s for %s: %d of %d records logged", wait, what, len(got), n)
		}
	}
}

// detectionFunc makes each connection's detector with its function.
type detectionFunc func() speech.Detector

func (f detectionFunc) Detector() speech.Detector { return f() }

// startingListening is how speech notices the user speak unless set otherwise.
var startingListening = speech.Listening{Threshold: 0.5, Pause: 200 * time.Millisecond, MaxWait: 3 * time.Second, InterruptSpeech: 200 * time.Millisecond, MaxSpeech: 2 * time.Minute}

// newListeningService returns the service over the fake transport, noticing
// speech with detection as the starting settings say and hearing nothing in
// it, and n of the caller's sessions.
func newListeningService(t *testing.T, detection speech.SpeechDetection, log *slog.Logger, n int) (*speech.Service, *fake.Transport, []uuid.UUID) {
	t.Helper()
	store := sessionsfake.New()
	var sessions []uuid.UUID
	for range n {
		s, _, err := store.Start(context.Background(), conversation.Session{ID: uuid.NewV7(), Workspace: workspace, User: user, Agent: keeperID, AgentVersion: 1}, uuid.NewV7(), "")
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		sessions = append(sessions, s.ID)
	}
	transport := fake.New()
	svc := speech.NewService(conversation.NewService(nil, store, nil, nil, nil, conversation.Settings{}), transport, nil, detection, sttfake.SpeechToText{}, endofturnfake.EndOfTurn{},
		speech.Settings{MaxConnections: n, Listening: startingListening}, log)
	t.Cleanup(func() { closeService(t, svc) })
	return svc, transport, sessions
}

func TestEachConnectionLogsWhatItNoticesInTheMicrophone(t *testing.T) {
	log := &logged{message: "speech noticed"}
	// Speech for 20 frames of 32 ms, then quiet.
	svc, transport, sessions := newListeningService(t, detectionfake.Detection{Script: slices.Repeat([]float32{0.9}, 20)}, slog.New(log), 1)
	connect(t, svc, sessions[0])
	// A second of microphone audio in 20 ms stretches, as the transport yields it.
	for range 50 {
		transport.Conn(0).Microphone(speech.UserAudio{PCM: make([]int16, 320)})
	}
	var got []string
	for _, r := range waitLogged(t, log, 3, "the speech and the pause to be logged") {
		if r["session"].String() != sessions[0].String() {
			t.Errorf("logged for session %s, want %s", r["session"], sessions[0])
		}
		got = append(got, r["noticed"].String()+"@"+r["audio_ms"].String())
	}
	// Held at 0.2 s, rounded up to a frame; the pause marks where speech ended.
	if want := []string{"speech@0", "held@224", "pause@640"}; !slices.Equal(got, want) {
		t.Errorf("logged %v, want %v", got, want)
	}
}

func TestAConnectionHoldsNoListenerUntilItsMicrophoneSendsAudio(t *testing.T) {
	made := make(chan struct{}, 10) // told of each listener made
	detection := detectionFunc(func() speech.Detector { made <- struct{}{}; return detectionfake.Detection{}.Detector() })
	svc, transport, sessions := newListeningService(t, detection, slog.New(slog.DiscardHandler), 2)
	connect(t, svc, sessions[0])
	connect(t, svc, sessions[1])
	transport.Conn(1).Microphone(speech.UserAudio{PCM: make([]int16, 320)})
	select {
	case <-made:
	case <-time.After(wait):
		t.Fatalf("waited %s for a listener for the connection with audio", wait)
	}
	closeService(t, svc) // every connection's serve loop has ended
	if more := len(made); more != 0 {
		t.Errorf("%d more listeners made, want only the one for the connection whose microphone sent audio", more)
	}
}
