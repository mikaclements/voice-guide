package speech_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/conversation"
	modelfake "github.com/mikaclements/voice-guide/internal/conversation/model/fake"
	"github.com/mikaclements/voice-guide/internal/speech"
	detectionfake "github.com/mikaclements/voice-guide/internal/speech/detection/fake"
	endofturnfake "github.com/mikaclements/voice-guide/internal/speech/endofturn/fake"
	sttfake "github.com/mikaclements/voice-guide/internal/speech/stt/fake"
	"github.com/mikaclements/voice-guide/internal/speech/transport/fake"
	ttsfake "github.com/mikaclements/voice-guide/internal/speech/tts/fake"
)

// speakFrames sends n frames of the microphone's audio; the detector's script says which are speech.
func speakFrames(c *fake.Connection, n int) {
	for range n {
		c.Microphone(speech.UserAudio{PCM: make([]int16, speech.DetectionFrame)})
	}
}

// saying hears the user say "Where should we start?", with rough text as it comes.
var saying = sttfake.SpeechToText{Rough: []string{"Where", "Where should"}, Final: "Where should we start?"}

// Speech for 5 frames, then quiet: the pause comes 7 quiet frames later.
var speechThenQuiet = detectionfake.Detection{Script: slices.Repeat([]float32{0.9}, 5)}

func TestASpokenTurnIsHeardThenAnsweredAsATypedOneIs(t *testing.T) {
	log := &logged{message: "turn spoken"}
	h := newHearingTurnHarness(t, &modelfake.Model{Pieces: replyPieces, Pace: 30 * time.Millisecond}, ttsfake.TextToSpeech{PacketsPerWord: 1},
		slog.New(log), nil, speechThenQuiet, saying, endofturnfake.EndOfTurn{Answer: true})
	c := h.connect(t)
	speakFrames(c, 12)

	got := until(t, c, "spoken")
	request := got[0].RequestID
	var types []string
	for _, m := range got {
		if m.RequestID != request {
			t.Errorf("got %+v, want every message about the spoken turn's request %s", m, request)
		}
		if m.Type == "hearing" {
			types = append(types, m.Type+":"+m.Text)
		} else if m.Type != "text" {
			types = append(types, m.Type)
		}
	}
	want := []string{"hearing:Where", "hearing:Where should", "hearing:Where should we start?", "hearing:Where should we start?", "done", "spoken"}
	if !slices.Equal(types, want) {
		t.Errorf("got %q, want the rough text, the final text, the turn taken with it, then the reply", types)
	}
	if taken := got[3]; !taken.Final || got[2].Final {
		t.Errorf("got %+v then %+v, want only the last hearing final", got[2], taken)
	}
	// The pause is 7 frames of quiet, 224 ms of audio before the turn was even taken.
	if spoken := got[len(got)-1]; spoken.FirstAudioMS < 224 {
		t.Errorf("spoken says the first audio went out %d ms after the speech ended, want at least the pause's 224", spoken.FirstAudioMS)
	}
	saved := h.turns.All()
	if len(saved) != 1 || saved[0].Words != "Where should we start?" || saved[0].RequestID.String() != request {
		t.Errorf("saved %+v, want the final text as the words of the spoken turn's request", saved)
	}

	closeService(t, h.svc) // returns once the turn has logged its stages
	stages, ok := log.first()
	if !ok || !stages["spoken"].Bool() || stages["turn_end"].String() != "check" {
		t.Fatalf("logged %v, want a spoken turn ended by the check", stages)
	}
	ms := func(stage string) int64 {
		if v := stages[stage]; v.Kind() == slog.KindInt64 {
			return v.Int64()
		}
		t.Errorf("%s wasn't logged", stage)
		return 0
	}
	if ms("taken_ms") < max(ms("final_text_ms"), ms("end_of_turn_check_ms"), 224) || ms("model_first_text_ms") < ms("taken_ms")+30 || ms("first_audio_sent_ms") < ms("model_first_text_ms") {
		t.Errorf("logged %v, want each stage from the end of speech: final text and check, taken after both and the pause, then the model's 30 ms, then audio", stages)
	}
}

// speakOverReply has the agent speak a typed turn's reply, a word every half second, and returns
// the connection once the reply is playing, with the typed turn's request ID.
func speakOverReply(t *testing.T, detection detectionfake.Detection) (turnHarness, *fake.Connection, uuid.UUID) {
	t.Helper()
	h := newHearingTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: slowWords},
		slog.New(slog.DiscardHandler), nil, detection, saying, endofturnfake.EndOfTurn{Answer: true})
	c := h.connect(t)
	typed := uuid.NewV7()
	c.Say(turnMessage(typed, "Hi."))
	until(t, c, "done")
	playedAtLeast(t, c, slowWords)
	return h, c, typed
}

func TestSpeechHeldOverTheReplyStopsItAndIsTakenAsTheNextTurn(t *testing.T) {
	// Speech for 20 frames, 640 ms: held at the 7th.
	h, c, typed := speakOverReply(t, detectionfake.Detection{Script: slices.Repeat([]float32{0.9}, 20)})
	speakFrames(c, 20)

	var cut message
	for cut.Type != "interrupted" {
		if cut = next(t, c); cut.Type == "hearing" && cut.Final {
			t.Fatalf("got %+v before the reply was interrupted, want it stopped once the speech was held, before the turn is taken", cut)
		}
	}
	if cut.RequestID != typed.String() {
		t.Errorf("got %+v, want the typed turn's reply interrupted", cut)
	}
	stopped := len(c.Played())
	speakFrames(c, 7) // the pause
	got := until(t, c, "spoken")
	if m := got[len(got)-1]; m.RequestID == typed.String() {
		t.Errorf("got %+v, want the spoken turn answered next", m)
	}
	saved := h.turns.All()
	if len(saved) != 2 || saved[0].Status != conversation.TurnCut || saved[1].Words != "Where should we start?" {
		t.Errorf("saved %+v, want the typed turn cut, then the spoken one", saved)
	}
	if n := len(c.Played()); n != stopped+4*slowWords {
		t.Errorf("%d packets played in all, want the typed reply's %d and the spoken one's %d: nothing of the first once it stopped", n, stopped, 4*slowWords)
	}
}

func TestShortSpeechOverTheReplyNeitherStopsItNorIsTaken(t *testing.T) {
	// Speech for 5 frames, 160 ms, short of the interrupt speech, then quiet: a click or a breath.
	h, c, typed := speakOverReply(t, speechThenQuiet)
	speakFrames(c, 5+7)

	got := until(t, c, "spoken")
	if len(got) != 1 || got[0].RequestID != typed.String() {
		t.Errorf("got %+v, want only the typed reply, played to its end", got)
	}
	if n := len(c.Played()); n != 4*slowWords {
		t.Errorf("%d packets played, want the whole reply's %d", n, 4*slowWords)
	}
	closeService(t, h.svc) // every turn begun is saved
	if saved := h.turns.All(); len(saved) != 1 || saved[0].Status != conversation.TurnAnswered {
		t.Errorf("saved %+v, want only the typed turn, answered", saved)
	}
}

// heldBack is speech-to-text that takes none of the audio until release is closed, as one far
// behind does, and says on dropped each time it takes an utterance's drop.
type heldBack struct {
	speech.SpeechToText
	release, dropped chan struct{}
}

func (h heldBack) Transcribe(ctx context.Context, audio <-chan speech.UtteranceAudio) <-chan speech.UtteranceText {
	held := make(chan speech.UtteranceAudio)
	// Ends with the audio or ctx; the stream it feeds closes only after, so whoever drains the
	// stream waits for it.
	go func() {
		defer close(held)
		select {
		case <-h.release:
		case <-ctx.Done():
			return
		}
		for a := range audio {
			select {
			case held <- a:
			case <-ctx.Done():
				return
			}
			if a.Drop {
				h.dropped <- struct{}{}
			}
		}
	}()
	return h.SpeechToText.Transcribe(ctx, held)
}

func TestSpeechToTextFallingBehindLosesOnlyTheUtteranceItCouldntTake(t *testing.T) {
	// 300 frames of speech, more than speech-to-text may fall behind by, then quiet, then 20 more.
	detection := detectionfake.Detection{Script: slices.Concat(slices.Repeat([]float32{0.9}, 300), make([]float32, 10), slices.Repeat([]float32{0.9}, 20))}
	stt := heldBack{SpeechToText: saying, release: make(chan struct{}), dropped: make(chan struct{}, 4)}
	h := newHearingTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1},
		slog.New(slog.DiscardHandler), nil, detection, stt, endofturnfake.EndOfTurn{Answer: true})
	c := h.connect(t)
	speakFrames(c, 310)
	// At its pause, the first utterance, past what speech-to-text could take, ends unheard.
	if lost := next(t, c); lost.Type != "hearing" || !lost.Final || !lost.Failed || lost.Text != "" {
		t.Errorf("got %+v, want the page told nothing of the first utterance was heard", lost)
	}
	close(stt.release)
	select {
	case <-stt.dropped: // caught up: it took the first utterance's drop, which waited for room
	case <-time.After(wait):
		t.Fatal("speech-to-text never took the first utterance's drop")
	}
	speakFrames(c, 27)

	got := until(t, c, "spoken")
	if saved := h.turns.All(); len(saved) != 1 || saved[0].Words != "Where should we start?" || saved[0].RequestID.String() != got[0].RequestID {
		t.Errorf("saved %+v, want the second utterance answered: speech-to-text numbers it as speech does", saved)
	}
}

func TestWithSpeechToTextDownFromTheStartEachPauseTellsThePageAndTypingStillWorks(t *testing.T) {
	// The speech-to-text service can't be reached, its end-of-turn check with it: every utterance fails.
	down := errors.New("speech-to-text can't be reached")
	stt := sttfake.SpeechToText{Fail: down, FailUtterances: []int{0, 1}}
	// Speech for 5 frames, quiet for 10, then speech for 5 again.
	detection := detectionfake.Detection{Script: slices.Concat(slices.Repeat([]float32{0.9}, 5), make([]float32, 10), slices.Repeat([]float32{0.9}, 5))}
	h := newHearingTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1},
		slog.New(slog.DiscardHandler), nil, detection, stt, endofturnfake.EndOfTurn{Fail: down})
	c := h.connect(t)
	for _, frames := range []int{5 + 10, 5 + 7} {
		speakFrames(c, frames)
		if m := next(t, c); m.Type != "hearing" || !m.Final || !m.Failed || m.Text != "" {
			t.Errorf("got %+v, want the page told at each pause that nothing was heard, so it offers typing", m)
		}
	}
	typed := uuid.NewV7()
	c.Say(turnMessage(typed, "Where should we start?"))
	until(t, c, "spoken")
	if saved := h.turns.All(); len(saved) != 1 || saved[0].RequestID != typed {
		t.Errorf("saved %+v, want only the typed turn", saved)
	}
}

func TestWithTheCheckDownASpokenTurnIsTakenByTheMaximumWait(t *testing.T) {
	log := &logged{message: "turn spoken"}
	h := newHearingTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1},
		slog.New(log), nil, speechThenQuiet, saying, endofturnfake.EndOfTurn{Fail: errors.New("the check can't be reached")})
	c := h.connect(t)
	// Speech for 5 frames, then the 3 s wait, 94 frames, counted from where it ended.
	speakFrames(c, 5+94)
	until(t, c, "spoken")
	closeService(t, h.svc) // returns once the turn has logged its stages
	stages, ok := log.first()
	if _, asked := stages["end_of_turn_check_ms"]; !ok || stages["turn_end"].String() != "wait" || asked {
		t.Errorf("logged %v, want a turn ended by the wait, with no answer from the check", stages)
	}
}

// unanswered is an end-of-turn check that never answers while the test runs.
type unanswered struct{}

func (unanswered) Finished(ctx context.Context, _ []int16) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func TestAConnectionEndingMidTurnTakesNothingHalfHeard(t *testing.T) {
	for name, tc := range map[string]struct {
		frames int
		heard  string
	}{
		"while the user speaks":          {frames: 3, heard: "Where should"},
		"while the check is still asked": {frames: 5 + 7, heard: "Where should we start?"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHearingTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1},
				slog.New(slog.DiscardHandler), nil, speechThenQuiet, saying, unanswered{})
			c := h.connect(t)
			speakFrames(c, tc.frames)
			for m := next(t, c); m.Text != tc.heard; m = next(t, c) {
			}
			h.transport.EndAll()
			closeService(t, h.svc) // every turn begun is saved
			if saved := h.turns.All(); len(saved) != 0 {
				t.Errorf("saved %+v, want nothing: the page keeps the words heard, to send or say again", saved)
			}
		})
	}
}
