package agents_test

import (
	"context"
	"math"
	"testing"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/agents/store/fake"
)

func TestPublishRefusesAVoiceThatIsNotAVoiceNameAndASpeedOutOfRange(t *testing.T) {
	for name, set := range map[string]func(*agents.Agent){
		"a path":            func(a *agents.Agent) { a.Voice = "../af_heart" },
		"capitals":          func(a *agents.Agent) { a.Voice = "AF_heart" },
		"no style":          func(a *agents.Agent) { a.Voice = "af_" },
		"a mix of voices":   func(a *agents.Agent) { a.Voice = "af_heart+bf_emma" },
		"a style over 32":   func(a *agents.Agent) { a.Voice = "af_" + "a123456789012345678901234567890123" },
		"a speed too slow":  func(a *agents.Agent) { a.Speed = 0.49 },
		"a speed too fast":  func(a *agents.Agent) { a.Speed = 2.01 },
		"a negative speed":  func(a *agents.Agent) { a.Speed = -1 },
		"a speed of no one": func(a *agents.Agent) { a.Speed = math.NaN() },
		"an endless speed":  func(a *agents.Agent) { a.Speed = math.Inf(1) },
	} {
		t.Run(name, func(t *testing.T) {
			a := lighthouseKeeper()
			set(&a)
			if _, err := agents.Publish(context.Background(), fake.New(), workspace, []agents.Agent{a}); err == nil {
				t.Error("Publish accepted it")
			}
		})
	}
}

func TestPublishAcceptsAVoiceAndASpeedAtEitherEndOfTheRange(t *testing.T) {
	for _, speed := range []float64{0, agents.MinSpeed, 1, agents.MaxSpeed} {
		a := lighthouseKeeper()
		a.Voice, a.Speed = "bf_emma", speed
		if _, err := agents.Publish(context.Background(), fake.New(), workspace, []agents.Agent{a}); err != nil {
			t.Errorf("Publish with speed %v = %v, want it accepted", speed, err)
		}
	}
}
