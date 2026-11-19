package speech_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/conversation"
	modelfake "github.com/mikaclements/voice-guide/internal/conversation/model/fake"
	endofturnfake "github.com/mikaclements/voice-guide/internal/speech/endofturn/fake"
	ttsfake "github.com/mikaclements/voice-guide/internal/speech/tts/fake"
)

// speechStages are the stages speech saves, in the order they come.
var speechStages = []string{"final_text", "end_of_turn_check", "taken", "first_phrase_ready", "first_audio_sent"}

// speechStagesOf returns the stages saved for the turn that speech added, by name, once the
// service has closed, which is once the turn's stage times are written.
func speechStagesOf(t *testing.T, h turnHarness, turn int) map[string]conversation.Stage {
	t.Helper()
	closeService(t, h.svc)
	saved := h.turns.All()
	if len(saved) <= turn {
		t.Fatalf("%d turns saved, want turn %d", len(saved), turn)
	}
	got := map[string]conversation.Stage{}
	for _, s := range h.turns.Stages(saved[turn].ID) {
		if slices.Contains(speechStages, s.Name) {
			got[s.Name] = s
		}
	}
	return got
}

// hasModelStages fails the test unless the turn's own prompt and save stages are saved, so a test
// that expects no audio stages can't pass because nothing was written.
func hasModelStages(t *testing.T, h turnHarness) {
	t.Helper()
	var got []string
	for _, s := range h.turns.Stages(h.turns.All()[0].ID) {
		got = append(got, s.Name)
	}
	for _, want := range []string{conversation.StagePrompt, conversation.StageSave} {
		if !slices.Contains(got, want) {
			t.Errorf("saved %v, want the turn's %s stage too", got, want)
		}
	}
}

func names(stages map[string]conversation.Stage) []string {
	var got []string
	for _, name := range speechStages {
		if _, ok := stages[name]; ok {
			got = append(got, name)
		}
	}
	return got
}

func TestASpokenTurnSavesSpeechsFiveStagesFromTheEndOfTheSpeech(t *testing.T) {
	h := newHearingTurnHarness(t, &modelfake.Model{Pieces: replyPieces, Pace: 30 * time.Millisecond}, ttsfake.TextToSpeech{PacketsPerWord: 1},
		slog.New(slog.DiscardHandler), nil, speechThenQuiet, saying, endofturnfake.EndOfTurn{Answer: true})
	c := h.connect(t)
	speakFrames(c, 12)
	until(t, c, "spoken")
	got := speechStagesOf(t, h, 0)
	if !slices.Equal(names(got), speechStages) {
		t.Fatalf("saved %v, want all five of speech's stages", names(got))
	}
	// The pause is 7 frames of quiet, 224 ms of audio, before the turn is taken; the model's first
	// piece takes 30 ms more, and the audio follows the phrase.
	taken, phrase, audio := got["taken"].Took, got["first_phrase_ready"].Took, got["first_audio_sent"].Took
	if taken < 224*time.Millisecond || phrase < taken+30*time.Millisecond || audio < phrase {
		t.Errorf("took taken %v, first phrase %v, first audio %v; want from the end of speech: the pause, then the model's 30 ms, then audio", taken, phrase, audio)
	}
	if got["final_text"].Took > taken || got["end_of_turn_check"].Took > taken {
		t.Errorf("final text %v and check %v, want both before the turn was taken at %v", got["final_text"].Took, got["end_of_turn_check"].Took, taken)
	}
	for name, s := range got {
		if !s.Started.Equal(got["taken"].Started) {
			t.Errorf("%s started at %v, want every stage counted from the same moment, %v", name, s.Started, got["taken"].Started)
		}
		if want := map[bool]string{true: "fake", false: ""}[name == "first_audio_sent"]; s.Implementation != want {
			t.Errorf("%s implementation %q, want %q", name, s.Implementation, want)
		}
	}
}

func TestATypedTurnOnTheAudioConnectionSavesOnlyTheTwoStagesItHad(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces, Pace: 30 * time.Millisecond}, ttsfake.TextToSpeech{PacketsPerWord: 1})
	c := h.connect(t)
	c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
	until(t, c, "spoken")
	got := speechStagesOf(t, h, 0)
	if want := []string{"first_phrase_ready", "first_audio_sent"}; !slices.Equal(names(got), want) {
		t.Fatalf("saved %v, want %v", names(got), want)
	}
	if phrase, audio := got["first_phrase_ready"].Took, got["first_audio_sent"].Took; phrase < 30*time.Millisecond || audio < phrase {
		t.Errorf("first phrase %v, first audio %v, want from the words arriving: at least the model's 30 ms, audio after the phrase", phrase, audio)
	}
}

func TestAReplyCutBeforeItsFirstAudioSavesNoAudioStages(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces, Pace: time.Hour}, ttsfake.TextToSpeech{PacketsPerWord: 1})
	c := h.connect(t)
	c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
	savedAtLeast(t, h.turns, 1)
	c.Say(turnMessage(uuid.NewV7(), "Actually, one thing first."))
	until(t, c, "interrupted")
	if got := speechStagesOf(t, h, 0); len(got) != 0 {
		t.Errorf("saved %v for a reply cut before any sound, want none", names(got))
	}
	hasModelStages(t, h)
}

func TestATextToSpeechFailureSavesNoAudioStages(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1, Fail: errors.New("text-to-speech is down")})
	c := h.connect(t)
	c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
	until(t, c, "spoken")
	if got := speechStagesOf(t, h, 0); len(got) != 0 {
		t.Errorf("saved %v with no audio made, want none", names(got))
	}
	hasModelStages(t, h)
}

func TestAReplyCutAfterItsFirstSoundStillSavesWhenTheAudioWentOut(t *testing.T) {
	held := heldPrompt{armed: &atomic.Bool{}, once: &sync.Once{}, release: make(chan struct{})}
	h := newLoggingTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: slowWords}, slog.New(slog.DiscardHandler),
		func(s conversation.Turns) conversation.Turns { held.Turns = s; return held })
	c := h.connect(t)
	c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
	until(t, c, "done")
	playedAtLeast(t, c, 5)
	held.armed.Store(true)
	c.Say(turnMessage(uuid.NewV7(), "Hold on."))
	until(t, c, "interrupted")
	close(held.release)
	until(t, c, "spoken")
	got := speechStagesOf(t, h, 0)
	if want := []string{"first_phrase_ready", "first_audio_sent"}; !slices.Equal(names(got), want) {
		t.Errorf("saved %v for a reply cut after its first sound, want %v", names(got), want)
	}
	hasModelStages(t, h)
}

// brokenFinish is a turn store that can't save how a turn ended, as when the database goes down
// after the reply began.
type brokenFinish struct{ conversation.Turns }

func (brokenFinish) Finish(context.Context, conversation.Turn) error {
	return errors.New("database is down")
}

func TestATurnWhoseSaveFailedHasNoStageTimesToWrite(t *testing.T) {
	log := &logged{message: "record a turn's stage times"}
	h := newLoggingTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1}, slog.New(log),
		func(s conversation.Turns) conversation.Turns { return brokenFinish{s} })
	c := h.connect(t)
	c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
	until(t, c, "spoken")
	closeService(t, h.svc)
	if got, ok := log.first(); ok {
		t.Errorf("logged %v, want no write of stage times for a turn that was never saved", got)
	}
}
