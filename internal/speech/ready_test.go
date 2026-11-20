package speech_test

import (
	"testing"
	"uuid"

	modelfake "github.com/mikaclements/voice-guide/internal/conversation/model/fake"
	ttsfake "github.com/mikaclements/voice-guide/internal/speech/tts/fake"
)

const warmMessage = `{"type":"warm"}`

func TestThePageAskingIsToldTheSessionIsReady(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1})
	c := h.connect(t)
	c.Say(warmMessage)
	if m := next(t, c); m.Type != "ready" {
		t.Fatalf("got %+v, want ready", m)
	}
	if saved, played := h.turns.All(), c.Played(); len(saved)+len(played) != 0 {
		t.Errorf("saved %+v and played %d packets, want nothing", saved, len(played))
	}
}

// A page asking twice on one connection is told once.
func TestThePageIsToldReadyOncePerConnection(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1})
	c := h.connect(t)
	c.Say(warmMessage)
	c.Say(warmMessage)
	if m := next(t, c); m.Type != "ready" {
		t.Fatalf("got %+v, want ready", m)
	}
	id := uuid.NewV7()
	c.Say(turnMessage(id, "Where should we start?"))
	for _, m := range until(t, c, "spoken") {
		if m.Type == "ready" {
			t.Errorf("got a second ready, want one per connection")
		}
	}
}
