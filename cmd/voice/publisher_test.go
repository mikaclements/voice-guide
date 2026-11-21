package main

import (
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/agents"
	agentsfake "github.com/mikaclements/voice-guide/internal/agents/store/fake"
	voicefake "github.com/mikaclements/voice-guide/internal/conversation/voicelist/fake"
)

// The script has no voice list with the fake text-to-speech and publishes any voice unchecked, so
// the route must too; with a real service's list, a voice it lacks is refused as the script refuses.
func TestTheRouteChecksVoicesOnlyWhereTheScriptDoes(t *testing.T) {
	agent := agents.Agent{ID: uuid.MustParse("01a1de8b-bba7-7000-bad0-0000000000a1"), Name: "Lighthouse keeper", Persona: "You keep the light.", Voice: "bf_emma"}
	list := voicefake.New("af_heart")
	for name, tc := range map[string]struct {
		tts     string
		refused bool
	}{"fake text-to-speech": {"fake", false}, "a real service": {"kokoro", true}} {
		t.Run(name, func(t *testing.T) {
			cfg, err := loadConfig(env(map[string]string{"VOICE_TTS": tc.tts}))
			if err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			_, err = newPublisher(cfg, agentsfake.New(), list).Publish(t.Context(), uuid.NewV7(), []agents.Agent{agent})
			if refused := err != nil; refused != tc.refused {
				t.Errorf("Publish = %v; want refused %v", err, tc.refused)
			}
		})
	}
}
