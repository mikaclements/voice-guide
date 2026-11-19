package speech_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	agentsfake "github.com/mikaclements/voice-guide/internal/agents/store/fake"
	"github.com/mikaclements/voice-guide/internal/conversation"
	modelfake "github.com/mikaclements/voice-guide/internal/conversation/model/fake"
	sessionsfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/speech"
	detectionfake "github.com/mikaclements/voice-guide/internal/speech/detection/fake"
	"github.com/mikaclements/voice-guide/internal/speech/transport/fake"
	ttsfake "github.com/mikaclements/voice-guide/internal/speech/tts/fake"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	itemsfake "github.com/mikaclements/voice-guide/internal/usercontext/store/fake"
)

// spokenAs is the text-to-speech fake, noting the voice and speed each reply is asked in.
type spokenAs struct {
	ttsfake.TextToSpeech
	mu    sync.Mutex
	asked [][2]any
}

func (s *spokenAs) Speak(ctx context.Context, voice string, speed float64, text <-chan string) <-chan speech.Packet {
	s.mu.Lock()
	s.asked = append(s.asked, [2]any{voice, speed})
	s.mu.Unlock()
	return s.TextToSpeech.Speak(ctx, voice, speed, text)
}

func (s *spokenAs) all() [][2]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][2]any(nil), s.asked...)
}

// speakingSession starts a session with the keeper, who has agent's voice and speed, over a
// service whose own voice is af_heart at its pace, and opens its audio connection.
func speakingSession(t *testing.T, agent agents.Agent, start conversation.StartRequest) (turnHarness, *fake.Connection, *spokenAs) {
	t.Helper()
	versions := agentsfake.New()
	agent.ID, agent.Name, agent.Persona = keeperID, "Lighthouse keeper", "You answer every call from the sea."
	if _, err := agents.Publish(context.Background(), versions, workspace, []agents.Agent{agent}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	sessions, turns := sessionsfake.New(), sessionsfake.NewTurns()
	model := &modelfake.Model{Pieces: replyPieces}
	conv := conversation.NewService(agents.NewCatalog(versions), sessions, turns, model, usercontext.NewService(itemsfake.New()),
		conversation.Settings{UserContextChars: 1000, SessionContextChars: 1000, ScreenInputChars: 1000, WordsChars: 1000, TurnDeadline: time.Minute, Model: "claude-test", Voice: "af_heart", Speed: 1})
	start.Agent, start.RequestID, start.User = keeperID, uuid.NewV7(), "aiko-42"
	s, _, err := conv.StartSession(context.Background(), product, start)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	tts := &spokenAs{TextToSpeech: ttsfake.TextToSpeech{PacketsPerWord: 1}}
	transport := fake.New()
	svc := speech.NewService(conv, transport, tts, detectionfake.Detection{}, nil, nil, speech.Settings{MaxConnections: 10, TextToSpeech: "fake", Listening: startingListening}, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { closeService(t, svc) })
	h := turnHarness{svc: svc, conv: conv, transport: transport, model: model, session: s.ID, caller: access.Caller{Workspace: workspace, User: s.User, Expires: expires}}
	return h, h.connect(t), tts
}

func TestEveryReplyIsSpokenInTheSessionsVoiceAndSpeed(t *testing.T) {
	two := 1.5
	for name, tt := range map[string]struct {
		agent agents.Agent
		start conversation.StartRequest
		voice string
		speed float64
	}{
		"the service's, when the agent and the start name none": {agents.Agent{}, conversation.StartRequest{}, "af_heart", 1},
		"the agent's":                  {agents.Agent{Voice: "bf_emma", Speed: 0.8}, conversation.StartRequest{}, "bf_emma", 0.8},
		"the start's over the agent's": {agents.Agent{Voice: "zf_xiaobei", Speed: 0.8}, conversation.StartRequest{Voice: "bf_emma", Speed: &two}, "bf_emma", 1.5},
	} {
		t.Run(name, func(t *testing.T) {
			_, c, tts := speakingSession(t, tt.agent, tt.start)
			for range 2 {
				c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
				until(t, c, "spoken")
			}
			want := [][2]any{{tt.voice, tt.speed}, {tt.voice, tt.speed}}
			if got := tts.all(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
				t.Errorf("replies asked for in %v, want both in %q at %v", got, tt.voice, tt.speed)
			}
		})
	}
}
