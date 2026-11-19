package conversation_test

import (
	"errors"
	"math"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/usercontext"
)

// withVoices returns the harness with a service that speaks in the server's af_heart at its own
// pace unless the agent or the start says otherwise.
func withVoices(t *testing.T) serviceOverFakes {
	t.Helper()
	h := newServiceOverFakes(t)
	h.settings.Voice, h.settings.Speed = "af_heart", 1
	h.svc = conversation.NewService(agents.NewCatalog(h.agents), h.sessions, h.turns, h.model, usercontext.NewService(h.items), h.settings)
	return h
}

func speed(f float64) *float64 { return &f }

func TestASessionSpeaksInTheServersVoiceUnlessTheAgentOrTheStartNamesOne(t *testing.T) {
	h := withVoices(t)
	h.publish(t, agents.Agent{ID: captainID, Name: "Ferry captain", Persona: "You know every crossing by heart.", Voice: "bf_emma", Speed: 1.25})

	server, _ := h.start(t, caller, keeperID, uuid.NewV7())
	agent, _ := h.start(t, caller, captainID, uuid.NewV7())
	started, _, err := h.startAs(caller, conversation.StartRequest{Agent: captainID, RequestID: uuid.NewV7(), Voice: "am_adam", Speed: speed(0.5)})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	for name, tt := range map[string]struct {
		got   conversation.Session
		voice string
		speed float64
	}{
		"the server's, when neither names one":      {server, "af_heart", 1},
		"the agent's, when the start names none":    {agent, "bf_emma", 1.25},
		"the start's over the agent's, each of two": {started, "am_adam", 0.5},
	} {
		if tt.got.Voice != tt.voice || tt.got.Speed != tt.speed {
			t.Errorf("%s: session speaks in %q at %v, want %q at %v", name, tt.got.Voice, tt.got.Speed, tt.voice, tt.speed)
		}
		if v, s := h.svc.Speaking(tt.got); v != tt.voice || s != tt.speed {
			t.Errorf("%s: Speaking = %q at %v, want %q at %v", name, v, s, tt.voice, tt.speed)
		}
	}
	// The start's voice only: the agent's speed still applies.
	voiceOnly, _, err := h.startAs(caller, conversation.StartRequest{Agent: captainID, RequestID: uuid.NewV7(), Voice: "af_heart"})
	if err != nil || voiceOnly.Voice != "af_heart" || voiceOnly.Speed != 1.25 {
		t.Errorf("a start naming a voice only = %q at %v (%v), want af_heart at the agent's 1.25", voiceOnly.Voice, voiceOnly.Speed, err)
	}
}

func TestASessionStartedBeforeSessionsKeptTheirVoiceSpeaksInTheServers(t *testing.T) {
	h := withVoices(t)
	if v, s := h.svc.Speaking(conversation.Session{}); v != "af_heart" || s != 1 {
		t.Errorf("Speaking = %q at %v, want the server's af_heart at 1", v, s)
	}
}

func TestAVoiceOrSpeedOutOfFormOrRangeIsRefusedAndNothingSaved(t *testing.T) {
	for name, r := range map[string]conversation.StartRequest{
		"a path":                {Voice: "../../etc/passwd"},
		"capitals":              {Voice: "AF_Heart"},
		"a mix of voices":       {Voice: "af_heart+bf_emma"},
		"no style":              {Voice: "af_"},
		"a speed just under":    {Speed: speed(0.49)},
		"a speed just over":     {Speed: speed(2.01)},
		"a speed of nothing":    {Speed: speed(0)},
		"a negative speed":      {Speed: speed(-1)},
		"a speed that's no one": {Speed: speed(nan())},
	} {
		t.Run(name, func(t *testing.T) {
			h := withVoices(t)
			r.Agent, r.RequestID = keeperID, uuid.NewV7()
			_, _, err := h.startAs(caller, r)
			if !errors.Is(err, conversation.ErrVoiceSettings) || h.sessions.Count() != 0 {
				t.Errorf("StartSession = %v, %d sessions; want ErrVoiceSettings and none saved", err, h.sessions.Count())
			}
		})
	}
}

func TestASpeedAtEitherEndOfTheRangeIsAccepted(t *testing.T) {
	for _, s := range []float64{agents.MinSpeed, agents.MaxSpeed} {
		h := withVoices(t)
		got, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), Speed: speed(s)})
		if err != nil || got.Speed != s {
			t.Errorf("a start at speed %v = %v at %v, want it accepted", s, err, got.Speed)
		}
	}
}

func TestARepeatedStartKeepsTheFirstStartsVoiceAndSpeed(t *testing.T) {
	h := withVoices(t)
	request := uuid.NewV7()
	first, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: request, Voice: "bf_emma", Speed: speed(1.5)})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	again, isNew, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: request, Voice: "am_adam", Speed: speed(0.7)})
	if err != nil || isNew || again.ID != first.ID || again.Voice != "bf_emma" || again.Speed != 1.5 {
		t.Errorf("the repeat = %+v, new %v, %v; want the first session, in bf_emma at 1.5", again, isNew, err)
	}
}

func nan() float64 { return math.NaN() }
