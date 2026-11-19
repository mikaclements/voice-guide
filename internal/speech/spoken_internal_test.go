package speech

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
)

// transcribing is speech-to-text that keeps the stream speech opens; the test reads the audio sent
// on it and plays its texts itself.
type transcribing struct {
	opened int
	audio  <-chan UtteranceAudio
	texts  chan UtteranceText
}

func (f *transcribing) Transcribe(_ context.Context, audio <-chan UtteranceAudio) <-chan UtteranceText {
	f.opened++
	f.audio = audio
	return f.texts
}

// asking is an end-of-turn check the test answers: each check comes out on asked with its audio,
// and waits for its own answer. With hold set, a check stopped before its answer returns only once
// hold is closed.
type asking struct {
	asked chan question
	hold  chan struct{}
}

type question struct {
	audio  []int16
	answer chan checkAnswer
}

func (a *asking) Finished(ctx context.Context, audio []int16) (bool, error) {
	q := question{audio: audio, answer: make(chan checkAnswer, 1)}
	a.asked <- q
	select {
	case c := <-q.answer:
		return c.finished, c.err
	case <-ctx.Done():
		if a.hold != nil {
			<-a.hold
		}
		return false, ctx.Err()
	}
}

// hearingHarness is one connection's spoken turns over a scripted detector, with speech-to-text
// and the end-of-turn check the test plays, the agent's reply the test sets, the page's messages
// recorded, and the turns taken.
type hearingHarness struct {
	st      *spokenTurns
	stt     *transcribing
	check   *asking
	page    *recorder
	replies *replies
	taken   chan heardTurn
	clock   *testClock
	ctx     context.Context
	at      int // samples heard so far
	stop    func()
}

// hearingStart is when the hearing harness's clock starts.
var hearingStart = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

func newHearingHarness(t *testing.T, script ...[]float32) *hearingHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	h := &hearingHarness{
		stt:     &transcribing{texts: make(chan UtteranceText)},
		check:   &asking{asked: make(chan question, 4)},
		page:    &recorder{},
		replies: &replies{caller: access.Caller{Expires: hearingStart.Add(4 * time.Hour)}}, // a ticket minted as the clock starts
		taken:   make(chan heardTurn, 4),
		clock:   &testClock{now: hearingStart},
	}
	s := &Service{stt: h.stt, endOfTurn: h.check, settings: Settings{Listening: listening}, log: slog.New(slog.DiscardHandler), clock: h.clock}
	h.ctx = ctx
	h.st = newSpokenTurns(s, h.page, uuid.NewV7(), h.replies)
	h.st.begin = func(_ context.Context, t heardTurn) { h.taken <- t }
	h.st.l = newListener(&scripted{script: slices.Concat(script...)}, listening, h.clock)
	// As the connection's loop ends: its context first, then the stream, which ends with its audio.
	var once sync.Once
	h.stop = func() {
		once.Do(func() {
			cancel()
			close(h.stt.texts)
			h.st.stop()
		})
	}
	t.Cleanup(h.stop)
	return h
}

// feed hears n frames, one at a time, each sample its place in the audio.
func (h *hearingHarness) feed(n int) {
	for range n {
		h.hearSamples(DetectionFrame)
	}
}

// hearSamples hears the next n samples, each its place in the audio.
func (h *hearingHarness) hearSamples(n int) {
	pcm := make([]int16, n)
	for i := range pcm {
		pcm[i] = int16((h.at + i) % 30000)
	}
	h.at += n
	h.st.hear(h.ctx, UserAudio{PCM: pcm})
}

// feedInTime hears n frames as they come in real time, the clock moving on a frame before each.
func (h *hearingHarness) feedInTime(n int) {
	for range n {
		h.clock.now = h.clock.now.Add(audioTime(DetectionFrame))
		h.feed(1)
	}
}

// answering has the agent answering a reply, which the returned turn stands for, with none of it
// heard yet: its context ends when the reply is told to stop.
func (h *hearingHarness) answering(t *testing.T) (*turnRun, context.Context) {
	speaking, stop := context.WithCancel(t.Context())
	run := &turnRun{interrupted: make(chan struct{}), stopSpeaking: stop, free: make(chan struct{})}
	h.replies.last = run
	return run, speaking
}

// speaking has the agent's reply reaching the user, its first packet sent at first, a minute ago if
// zero, and its last at last, or still playing if last is zero.
func (h *hearingHarness) speaking(t *testing.T, first, last time.Time) *turnRun {
	run, _ := h.answering(t)
	if first.IsZero() {
		first = h.clock.now.Add(-time.Minute)
	}
	h.replies.aired.note(run, first, true)
	if !last.IsZero() {
		h.replies.aired.note(run, last, true)
		h.replies.aired.note(run, last, false)
	}
	return run
}

// pieces takes the audio sent to speech-to-text so far.
func (h *hearingHarness) pieces() []UtteranceAudio {
	var got []UtteranceAudio
	for {
		select {
		case a := <-h.stt.audio:
			got = append(got, a)
		default:
			return got
		}
	}
}

// asked returns the next check asked.
func (h *hearingHarness) asked(t *testing.T) question {
	t.Helper()
	select {
	case q := <-h.check.asked:
		return q
	case <-time.After(time.Second):
		t.Fatal("the end-of-turn check wasn't asked")
		return question{}
	}
}

// answer answers the next check asked, and passes the answer on as the connection's loop does.
func (h *hearingHarness) answer(t *testing.T, finished bool, err error) {
	t.Helper()
	h.asked(t).answer <- checkAnswer{finished: finished, err: err}
	select {
	case c := <-h.st.checks:
		h.st.receiveCheck(h.ctx, c)
	case <-time.After(time.Second):
		t.Fatal("the check's answer didn't come back")
	}
}

// say plays a text from speech-to-text about utterance n.
func (h *hearingHarness) say(n int, text string, final bool) {
	h.st.text(h.ctx, UtteranceText{Utterance: n, Text: text, Final: final})
}

// turn returns the turn taken, if one was.
func (h *hearingHarness) turn() (heardTurn, bool) {
	select {
	case got := <-h.taken:
		return got, true
	default:
		return heardTurn{}, false
	}
}

// heard lists the page's hearing messages: their words, and final for the last.
func (h *hearingHarness) heard(t *testing.T) []string {
	t.Helper()
	var got []string
	for _, raw := range h.page.messages {
		var m out
		if err := json.Unmarshal([]byte(raw), &m); err != nil || m.Type != "hearing" {
			t.Fatalf("the page got %s, want only hearing messages", raw)
		}
		if m.Final {
			m.Text += " (final)"
		}
		if m.Failed {
			m.Text += " (failed)"
		}
		got = append(got, m.Text)
	}
	return got
}

// Quiet for 20 frames, speech for 5, then quiet: the pause is noticed in the 7th quiet frame.
var speakingOnce = [][]float32{frames(0.1, 20), frames(0.9, 5)}

func TestSpeechGoesToSpeechToTextFromJustBeforeItBegan(t *testing.T) {
	h := newHearingHarness(t, speakingOnce...)
	h.feed(20)
	if h.stt.opened != 0 {
		t.Fatalf("speech-to-text opened before any speech")
	}
	h.feed(2)
	got := h.pieces()
	// Speech began at sample 10240; 0.3 s before it is 4800 samples earlier.
	if len(got) != 2 || got[0].Samples[0] != 5440 || len(got[0].Samples) != 4800+DetectionFrame || len(got[1].Samples) != DetectionFrame || got[1].Samples[0] != 10752 {
		t.Fatalf("sent %d pieces, the first from %d; want the audio from sample 5440, then each frame", len(got), got[0].Samples[0])
	}
	h.feed(3 + 20 + 5)
	if h.stt.opened != 1 {
		t.Errorf("speech-to-text opened %d times, want once for the connection", h.stt.opened)
	}
}

func TestAPauseEndsTheUtteranceAndAsksTheCheckAboutTheLastEightSeconds(t *testing.T) {
	// Ten seconds of quiet first, so more than 8 s has been heard.
	h := newHearingHarness(t, frames(0.1, 313), frames(0.9, 5))
	h.feed(313 + 5 + 6)
	if got := h.pieces(); slices.ContainsFunc(got, func(a UtteranceAudio) bool { return a.End }) {
		t.Fatal("the utterance ended after six quiet frames, before the pause")
	}
	h.feed(1)
	got := h.pieces()
	if len(got) != 1 || !got[0].End || len(got[0].Samples) != DetectionFrame {
		t.Errorf("at the pause sent %+v, want the last frame with the utterance's end", got)
	}
	if audio := h.asked(t).audio; len(audio) != int(samples(TurnAudio)) {
		t.Errorf("the check was asked about %d samples, want the last 8 s, %d, before any final text", len(audio), samples(TurnAudio))
	}
}

func TestATurnIsTakenOnceItsFinalTextIsInAndTheCheckSaysFinished(t *testing.T) {
	for _, finalFirst := range []bool{false, true} {
		h := newHearingHarness(t, speakingOnce...)
		h.feed(25)
		h.say(0, "Where should", false)
		h.feed(7)
		if finalFirst {
			h.say(0, "Where should we start?", true)
		} else {
			h.answer(t, true, nil)
		}
		if _, ok := h.turn(); ok {
			t.Fatalf("taken with only one of the final text and the check's answer in (final first: %v)", finalFirst)
		}
		if finalFirst {
			h.answer(t, true, nil)
		} else {
			h.say(0, "Where should we start?", true)
		}
		got, ok := h.turn()
		if !ok || got.words != "Where should we start?" || got.by != endedByCheck {
			t.Errorf("took %+v (%v), want the final text, ended by the check (final first: %v)", got, ok, finalFirst)
		}
		want := []string{"Where should", "Where should we start?", "Where should we start? (final)"}
		if heard := h.heard(t); !slices.Equal(heard, want) {
			t.Errorf("the page heard %q, want %q", heard, want)
		}
	}
}

func TestSpeakingAgainAfterAPauseContinuesTheTurn(t *testing.T) {
	h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 5), frames(0.1, 10), frames(0.9, 5))
	h.feed(32)
	h.say(0, "I led the team", true)
	h.asked(t)
	h.feed(3 + 5) // speaking again while the check runs: its answer no longer counts
	h.st.receiveCheck(h.ctx, checkAnswer{n: 1, finished: true})
	h.feed(7)
	h.say(1, "that rebuilt pricing.", true)
	if _, ok := h.turn(); ok {
		t.Fatal("taken on the check asked before the user spoke again")
	}
	h.answer(t, true, nil)
	got, ok := h.turn()
	if !ok || got.words != "I led the team that rebuilt pricing." {
		t.Errorf("took %+v, want both utterances' final texts joined", got)
	}
	for _, raw := range h.page.messages {
		var m out
		if json.Unmarshal([]byte(raw), &m) != nil || m.RequestID != got.request.String() {
			t.Errorf("the page got %s, want every message about the turn's own request, %s", raw, got.request)
		}
	}
}

func TestTheMaximumWaitTakesTheTurnWhenTheCheckDoesnt(t *testing.T) {
	for name, answer := range map[string]func(h *hearingHarness){
		"it says not finished": func(h *hearingHarness) { h.answer(t, false, nil) },
		"it fails":             func(h *hearingHarness) { h.answer(t, true, errors.New("the check is down")) },
		"it is late":           func(h *hearingHarness) { h.asked(t) },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHearingHarness(t, speakingOnce...)
			h.feed(32)
			h.say(0, "Let me think", true)
			answer(h)
			// The wait is up 94 frames after speech ended, 87 after the pause.
			h.feed(86)
			if _, ok := h.turn(); ok {
				t.Fatal("taken before the maximum wait")
			}
			h.feed(1)
			if got, ok := h.turn(); !ok || got.words != "Let me think" || got.by != endedByWait {
				t.Errorf("took %+v (%v), want the final text, ended by the wait", got, ok)
			}
		})
	}
}

func TestATurnWithNoWordsIsntTaken(t *testing.T) {
	for name, hear := range map[string]func(h *hearingHarness){
		"nothing heard": func(h *hearingHarness) { h.say(0, "", true) },
		"rough text, then none final": func(h *hearingHarness) {
			h.say(0, "Hm", false)
			h.say(0, "", true)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHearingHarness(t, speakingOnce...)
			h.feed(32)
			hear(h)
			h.answer(t, true, nil)
			if got, ok := h.turn(); ok {
				t.Errorf("took %+v, want nothing taken", got)
			}
			// A page shown rough text is told the turn came to nothing.
			if heard := h.heard(t); len(heard) > 0 && heard[len(heard)-1] != " (final)" {
				t.Errorf("the page heard %q, want it told the turn's words are none", heard)
			}
		})
	}
}

func TestASpokenTurnsStagesAreTimedFromWhereTheSpeechEnded(t *testing.T) {
	h := newHearingHarness(t, speakingOnce...)
	start := h.clock.now
	h.feed(32)
	// The pause was noticed at the end of sample 16384, 3584 samples after speech ended at 12800.
	ended := start.Add(-224 * time.Millisecond)
	h.clock.now = start.Add(100 * time.Millisecond)
	h.say(0, "Where should we start?", true)
	h.clock.now = start.Add(150 * time.Millisecond)
	h.answer(t, true, nil)
	got, ok := h.turn()
	if !ok || !got.ended.Equal(ended) || !got.finalText.Equal(start.Add(100*time.Millisecond)) || !got.checked.Equal(start.Add(150*time.Millisecond)) {
		t.Errorf("took %+v (%v), want the speech ended at %v, final text 100 ms and the check's answer 150 ms after the pause", got, ok, ended)
	}
}

func TestStoppingClosesTheStreamAndWaitsForTheCheckInFlight(t *testing.T) {
	h := newHearingHarness(t, speakingOnce...)
	h.check.hold = make(chan struct{})
	h.feed(32)
	h.asked(t)
	stopped := make(chan struct{})
	go func() {
		h.stop()
		close(stopped)
	}()
	// Far longer than stopping takes when it doesn't wait (microseconds), short enough to keep the
	// test quick.
	select {
	case <-stopped:
		t.Fatal("stopped while the check was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(h.check.hold)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("didn't stop once the check had returned")
	}
	for open := true; open; {
		select {
		case _, open = <-h.stt.audio:
		default:
			t.Fatal("the stream's audio is still open once stopped")
		}
	}
}

func TestSpeechResumingRightAfterAPauseSendsNoAudioTwice(t *testing.T) {
	h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 5), frames(0.1, 7), frames(0.9, 2))
	h.feed(32)
	h.pieces() // the first utterance, sent to the end of the frame its pause was noticed in, 16384
	h.feed(1)
	if got := h.pieces(); len(got) != 1 || got[0].Samples[0] != 16384 || len(got[0].Samples) != DetectionFrame {
		t.Errorf("sent %d pieces, the first %d samples from %d; want only the new frame, from 16384", len(got), len(got[0].Samples), got[0].Samples[0])
	}
}

func TestATurnSpeechToTextCantHearInFullGivesThePageTheWordsHeard(t *testing.T) {
	lost := errors.New("speech-to-text is down")
	for name, tc := range map[string]struct {
		hear func(h *hearingHarness)
		want []string
	}{
		"failing in the second utterance": {func(h *hearingHarness) {
			h.say(0, "I led the team", true)
			h.feed(3 + 5)
			h.say(1, "that rebuilt", false)
			h.st.text(h.ctx, UtteranceText{Utterance: 1, Err: lost})
		}, []string{"I led the team", "I led the team that rebuilt", "I led the team that rebuilt (final) (failed)"}},
		"failing before any words": {func(h *hearingHarness) {
			h.st.text(h.ctx, UtteranceText{Utterance: 0, Err: lost})
		}, []string{" (final) (failed)"}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 5), frames(0.1, 10), frames(0.9, 5))
			h.feed(32)
			tc.hear(h)
			// Ended at the pause, without waiting for the check: no turn can be taken.
			h.feed(7)
			if got, ok := h.turn(); ok {
				t.Errorf("took %+v, want nothing taken", got)
			}
			if heard := h.heard(t); !slices.Equal(heard, tc.want) {
				t.Errorf("the page heard %q, want %q: the words heard so far, kept for the user", heard, tc.want)
			}
		})
	}
}

func TestAnEndThatFindsTheQueueFullWaitsForRoomAndItsTurnIsHeard(t *testing.T) {
	// 250 frames of speech and the six quiet frames after fill the queue to its 256 pieces exactly,
	// so the seventh quiet frame's end finds it full.
	h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 250))
	h.feed(20 + 250 + 7)
	if got := h.pieces(); len(got) != sttAhead || slices.ContainsFunc(got, func(a UtteranceAudio) bool { return a.End || a.Drop }) {
		t.Fatalf("sent %d pieces, want the utterance's %d pieces of audio and no end yet", len(got), sttAhead)
	}
	h.st.flush() // speech-to-text has room again
	if got := h.pieces(); len(got) != 1 || !got[0].End || len(got[0].Samples) != DetectionFrame {
		t.Fatalf("then sent %+v, want the end held back, with the last frame", got)
	}
	h.say(0, "Where should we start?", true)
	h.answer(t, true, nil)
	if got, ok := h.turn(); !ok || got.words != "Where should we start?" {
		t.Errorf("took %+v (%v), want the turn taken: all its audio got through", got, ok)
	}
}

func TestAnUtteranceStartingWhileADropIsHeldGoesBehindIt(t *testing.T) {
	h := newHearingHarness(t, frames(0.9, 300), frames(0.1, 10), frames(0.9, 5))
	h.feed(300 + 7) // the first utterance outgrows the queue and is dropped; its drop is held
	for range 10 {
		<-h.stt.audio // speech-to-text takes a little
	}
	h.feed(3 + 1) // the next utterance begins
	got := h.pieces()
	if n := len(got); n < sttAhead-10+2 || !got[sttAhead-10].Drop || len(got[sttAhead-10+1].Samples) == 0 {
		t.Fatalf("after the first utterance's audio got %d pieces, want its drop, then the next utterance's audio", n-(sttAhead-10))
	}
}

func TestATurnsSpeechReachingTheMostItMayHoldIsTakenWithWhatWasHeard(t *testing.T) {
	// Quiet for 20 frames, speech that doesn't pause for 60, then after a pause speech again.
	h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 60), frames(0.1, 10), frames(0.9, 5))
	h.st.s.settings.Listening.MaxSpeech = time.Second
	// The speech began at sample 10240, its audio sent from 0.3 s before, 5440: a second of audio
	// from there ends at 21440, in the 42nd frame.
	h.feed(41)
	sent := h.pieces()
	h.feed(1)
	sent = append(sent, h.pieces()...)
	total := 0
	for _, a := range sent {
		total += len(a.Samples)
	}
	if end := sent[len(sent)-1].End; !end || total != int(samples(time.Second)) {
		t.Fatalf("sent %d samples, the utterance ended: %v; want a second of audio, then its end", total, end)
	}
	h.feed(80 - 42)
	if got := h.pieces(); len(got) > 0 {
		t.Errorf("sent %d pieces more as the speech went on, want none: the rest of it isn't heard", len(got))
	}
	h.say(0, "I kept on talking", true)
	got, ok := h.turn()
	if !ok || got.words != "I kept on talking" || got.by != endedByMaxSpeech {
		t.Errorf("took %+v (%v), want the words heard taken, ended by the most a turn's speech may hold", got, ok)
	}
	select {
	case <-h.check.asked:
		t.Error("the end-of-turn check was asked, want the turn taken without it")
	default:
	}
	h.feed(7 + 3 + 1) // the pause, then speech again
	if got := h.pieces(); len(got) == 0 || got[0].End || len(got[0].Samples) == 0 {
		t.Errorf("then sent %+v, want speaking again after a pause heard as a new turn", got)
	}
}

func TestTheMostATurnsSpeechMayHoldCountsThePausesWithinIt(t *testing.T) {
	// Quiet for 20 frames, speech for 10, quiet for 10, then speech that goes on.
	h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 10), frames(0.1, 10), frames(0.9, 40))
	h.st.s.settings.Listening.MaxSpeech = time.Second
	h.feed(20 + 10 + 7) // the pause
	h.answer(t, false, nil)
	// The turn's audio went from 5440, 0.3 s before its speech began: a second of it ends at 21440,
	// in the 42nd frame, the second utterance's third.
	h.feed(3 + 2)
	var total, ends int
	for _, a := range h.pieces() {
		total += len(a.Samples)
		if a.End {
			ends++
		}
	}
	if total != int(samples(time.Second)) || ends != 2 {
		t.Fatalf("sent %d samples and %d ends, want a second of audio from the turn's first, pause included, in two utterances", total, ends)
	}
	h.say(0, "I led the team", true)
	h.say(1, "that rebuilt pricing", true)
	if got, ok := h.turn(); !ok || got.words != "I led the team that rebuilt pricing" || got.by != endedByMaxSpeech {
		t.Errorf("took %+v (%v), want both utterances, ended by the most a turn's speech may hold", got, ok)
	}
}

func TestSpeechAfterATurnClosedAtTheMostIsANewTurnBeforeTheClosedOnesTextComes(t *testing.T) {
	// Quiet for 20 frames, speech running past the most, quiet for 10, then speech again.
	h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 40), frames(0.1, 10), frames(0.9, 5))
	h.st.s.settings.Listening.MaxSpeech = time.Second
	h.feed(20 + 40 + 10 + 5 + 7) // closed at the 42nd frame; the next speech, then its pause
	h.say(1, "And one more thing.", true)
	h.answer(t, true, nil)
	if got, ok := h.turn(); ok {
		t.Fatalf("took %+v before the turn closed at the most, want that one first", got)
	}
	h.say(0, "I kept on talking", true) // the closed turn's final text, late
	first, _ := h.turn()
	second, _ := h.turn()
	if first.words != "I kept on talking" || first.by != endedByMaxSpeech || second.words != "And one more thing." {
		t.Errorf("took %q (%s), then %q; want the closed turn, then the speech after it as a turn of its own", first.words, first.by, second.words)
	}
}

func TestASpokenTurnHasBegunWhenItIsHandedOver(t *testing.T) {
	h := newHearingHarness(t, speakingOnce...)
	// The connection's turns over a connection that has ended, so a turn begun goes no further.
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	var turns sync.WaitGroup
	t.Cleanup(turns.Wait)
	h.replies.s, h.replies.conn, h.replies.turns, h.replies.waiting = h.st.s, h.page, &turns, make(chan struct{}, waitingTurns)
	h.st.begin = func(_ context.Context, t heardTurn) { h.replies.beginSpoken(ended, t) }
	h.feed(32)
	h.say(0, "Where should we start?", true)
	h.answer(t, true, nil)
	var taken out
	if err := json.Unmarshal([]byte(h.page.messages[len(h.page.messages)-1]), &taken); err != nil || !taken.Final {
		t.Fatalf("the page was last told %+v (%v), want the turn taken", taken, err)
	}
	h.replies.mu.Lock()
	last := h.replies.last
	h.replies.mu.Unlock()
	if last == nil || last.request.String() != taken.RequestID {
		t.Errorf("the last turn begun is %+v, want the spoken one, begun by the time it was handed over", last)
	}
}

func TestSpeechHeldOverTheAgentStopsItsReplyWithinAFrameOfTheInterruptSpeech(t *testing.T) {
	// Quiet for 10 frames, then the user speaks on.
	h := newHearingHarness(t, frames(0.1, 10), frames(0.9, 100))
	run, speaking := h.answering(t)
	start := h.clock.now
	// The reply plays a packet every 20 ms, and the user's audio arrives at the same pace: each
	// packet's 20 ms of it as the packet goes.
	conn := &track{clock: h.clock, afterPlay: func(int) { h.hearSamples(320) }}
	packets := make(chan Packet, 200)
	for range cap(packets) {
		packets <- Packet{Opus: []byte{0xf8, 0xff, 0xfe}}
	}
	close(packets)
	p := play(speaking, conn, packets, h.clock, func(at time.Time, on bool) { h.replies.aired.note(run, at, on) })

	// The speech began at sample 5120, which arrived with the 17th packet, 320 ms in. It was held at
	// the end of the frame that completes 0.2 s of it, 224 ms, which arrived with the 28th packet,
	// the rest of whose 20 ms of audio came with it.
	began, last := start.Add(320*time.Millisecond), p.sent[len(p.sent)-1].at
	stopped := last.Add(PacketDuration) // the end of the last packet's audio
	if d, frame := stopped.Sub(began), audioTime(DetectionFrame); d < listening.InterruptSpeech || d > listening.InterruptSpeech+frame+PacketDuration {
		t.Errorf("the reply's audio stopped %s after the speech began, want within a frame, %s, and the microphone packet carrying it, %s, of the interrupt speech, %s", d, frame, PacketDuration, listening.InterruptSpeech)
	}
	// The held speech reached voice with that packet, and was said the microphone's capture before.
	if at, said := run.interruptedAt(); !said || !at.Equal(last.Add(-captureDelay)) {
		t.Errorf("interrupted at %v (%v), want when the held speech was said, %v: the part heard is worked out from it", at, said, last.Add(-captureDelay))
	}
}

func TestShortSpeechIsPassedOverOnlyWhileTheAgentIsHeardThroughIt(t *testing.T) {
	start := hearingStart
	// The reply's last packet goes at 700 ms: with its 20 ms and the browser's 40, it is heard until
	// 760 ms, plus the round trip, by what reaches voice 30 ms after it is said.
	ends := start.Add(700 * time.Millisecond)
	for name, tc := range map[string]struct {
		agent     func(t *testing.T, h *hearingHarness) *turnRun
		roundTrip time.Duration
		over      bool // it began while the reply reached the user, so its words wait to be shown
		taken     bool
	}{
		"in the middle of the reply":  {agent: func(t *testing.T, h *hearingHarness) *turnRun { return h.speaking(t, time.Time{}, time.Time{}) }, over: true},
		"overlapping the reply's end": {agent: func(t *testing.T, h *hearingHarness) *turnRun { return h.speaking(t, time.Time{}, ends) }, over: true, taken: true},
		// The reply's end reaches the user 100 ms later, after the speech has ended.
		"overlapping the reply's end, a round trip away": {agent: func(t *testing.T, h *hearingHarness) *turnRun { return h.speaking(t, time.Time{}, ends) }, roundTrip: 100 * time.Millisecond, over: true},
		"before the reply's first audio": {agent: func(t *testing.T, h *hearingHarness) *turnRun {
			run, _ := h.answering(t)
			return run
		}, taken: true},
		// Sent at 620 ms, the reply's first packet is heard only from 660 ms.
		"as the reply's first packet goes, before it is heard": {agent: func(t *testing.T, h *hearingHarness) *turnRun {
			return h.speaking(t, start.Add(620*time.Millisecond), time.Time{})
		}, taken: true},
		"after the reply has ended": {agent: func(t *testing.T, h *hearingHarness) *turnRun {
			return h.speaking(t, time.Time{}, start.Add(-10*time.Second))
		}, taken: true},
	} {
		t.Run(name, func(t *testing.T) {
			// Quiet for 20 frames, then "Yes." for 6, 192 ms, short of the interrupt speech, heard from
			// 640 ms to 832 ms in.
			h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 6))
			h.page.roundTrip = tc.roundTrip
			run := tc.agent(t, h)
			h.feedInTime(26)
			h.say(0, "Yes", false)
			if shown := len(h.page.messages) > 0; shown == tc.over {
				t.Errorf("the page was told of it as it was heard: %v, want that only for speech not begun over the agent", shown)
			}
			h.feedInTime(7) // the pause
			if _, cut := run.interruptedAt(); cut {
				t.Error("the reply was interrupted by speech shorter than the interrupt speech")
			}
			if tc.taken {
				h.say(0, "Yes.", true)
				h.answer(t, true, nil)
				if got, ok := h.turn(); !ok || got.words != "Yes." {
					t.Errorf("took %+v (%v), want the speech taken as a turn: the agent wasn't heard all through it", got, ok)
				}
				return
			}
			select {
			case <-h.check.asked:
				t.Error("the end-of-turn check was asked about speech passed over")
			default:
			}
			h.say(0, "Yes.", true) // its final text comes after it was passed over, to nothing
			if got, ok := h.turn(); ok || len(h.page.messages) != 0 {
				t.Errorf("took %+v (%v) and told the page %q, want nothing of speech passed over", got, ok, h.page.messages)
			}
		})
	}
}

func TestABlipOverTheAgentIsPassedOverAndOpensNothingAfterIt(t *testing.T) {
	// A blip of 5 frames, 160 ms, over the reply; the pause; then held speech 3 frames later.
	h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 5), frames(0.1, 10), frames(0.9, 20))
	run := h.speaking(t, time.Time{}, time.Time{})
	h.feed(20 + 5 + 10)
	if _, cut := run.interruptedAt(); cut || h.st.turn != nil {
		t.Fatalf("after the blip and its pause: interrupted %v, a turn open %v; want the blip passed over at once", cut, h.st.turn != nil)
	}
	if sent := h.pieces(); len(sent) == 0 || !sent[len(sent)-1].Drop || sent[len(sent)-1].End {
		t.Errorf("sent speech-to-text %+v, want the blip's utterance dropped at its pause, its final text never asked for", sent)
	}
	h.feed(20) // held in its 7th frame
	if _, cut := run.interruptedAt(); !cut {
		t.Fatal("not interrupted once the speech after the blip was held")
	}
	h.say(0, "Uh—", true)
	h.say(1, "go back.", true)
	h.feed(7) // the pause
	h.answer(t, true, nil)
	if got, ok := h.turn(); !ok || got.words != "go back." {
		t.Errorf("took %+v (%v), want only the held speech: nothing kept from the blip", got, ok)
	}
	for _, heard := range h.heard(t) {
		if strings.Contains(heard, "Uh") {
			t.Errorf("the page was told %q, want nothing of the blip", heard)
		}
	}
}

func TestTwoTapsOverTheAgentDontCutIn(t *testing.T) {
	// A frame of speech, five quiet, a frame of speech: one utterance spanning 0.2 s, two frames of it
	// speech, then the pause.
	h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 1), frames(0.1, 5), frames(0.9, 1), frames(0.1, 7))
	run := h.speaking(t, time.Time{}, time.Time{})
	h.feed(20 + 1 + 5 + 1 + 7)
	if _, cut := run.interruptedAt(); cut || h.st.turn != nil {
		t.Errorf("interrupted %v, a turn open %v; want two taps over the agent passed over", cut, h.st.turn != nil)
	}
}

func TestSpeechRightAfterABlipPassedOverOpensWithTheAudioAfterTheBlip(t *testing.T) {
	// A blip over the reply ends at sample 12800, and its pause is noticed at 16384; speech begins a
	// frame later, at 16896, so 0.3 s before it, 12096, falls inside the blip.
	h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 5), frames(0.1, 8), frames(0.9, 20))
	h.speaking(t, time.Time{}, time.Time{})
	h.feed(20 + 5 + 7)
	h.pieces() // the blip's audio, then its drop
	h.feed(1 + 1)
	got := h.pieces()
	if len(got) == 0 || got[0].Samples[0] != 12800 || len(got[0].Samples) != 16896+DetectionFrame-12800 {
		t.Errorf("sent %d pieces, the first %+v; want the audio from where the blip ended, 12800, to the end of the speech's first frame", len(got), got)
	}
}

func TestHeldNoiseOverTheAgentCutsInAndTakesNoTurnWithoutWords(t *testing.T) {
	// A cough or a burst of noise held 10 frames over the reply, in which speech-to-text hears nothing.
	h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 10), frames(0.1, 7))
	run := h.speaking(t, time.Time{}, time.Time{})
	h.feed(20 + 10)
	if _, cut := run.interruptedAt(); !cut {
		t.Fatal("not interrupted by noise over the agent held for the interrupt speech")
	}
	h.feed(7) // the pause
	h.say(0, "", true)
	h.answer(t, true, nil)
	if got, ok := h.turn(); ok || h.st.turn != nil || len(h.page.messages) != 0 {
		t.Errorf("took %+v (%v), a turn left open %v, told the page %q; want no turn taken and no hearing message: the agent waits for the user", got, ok, h.st.turn != nil, h.page.messages)
	}
}

func TestAShortWordOverTheAgentCutsInOnItsOwnAndWhatFollowsJoinsItsTurn(t *testing.T) {
	// "No—" for 8 frames, 256 ms, over the reply; a pause the check says isn't the end; "go back."
	h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 8), frames(0.1, 10), frames(0.9, 20))
	run := h.speaking(t, time.Time{}, time.Time{})
	h.feed(20 + 8) // held in its 7th frame
	if _, cut := run.interruptedAt(); !cut {
		t.Fatal("not interrupted by a word over the agent lasting the interrupt speech")
	}
	h.say(0, "No—", true)
	h.feed(10) // the pause
	h.answer(t, false, nil)
	h.feed(20)
	h.say(1, "go back.", true)
	h.feed(7) // the pause
	h.answer(t, true, nil)
	if got, ok := h.turn(); !ok || got.words != "No— go back." {
		t.Errorf("took %+v (%v), want one turn of both: the word that cut in, then what followed it", got, ok)
	}
}

func TestHeldSpeechBeforeTheReplysFirstAudioDoesntCutIn(t *testing.T) {
	h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 20))
	run, _ := h.answering(t)
	h.feed(20 + 20)
	if _, cut := run.interruptedAt(); cut {
		t.Error("the reply was interrupted before any of it reached the user, want that left to the turn when taken")
	}
}

func TestSpeechPassedOverThatSpeechToTextLostDoesntFailTheTurnAfterIt(t *testing.T) {
	// A blip over the reply, lost by speech-to-text, then "Yes." over the reply's end at 1.2 s.
	h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 5), frames(0.1, 10), frames(0.9, 6))
	h.speaking(t, time.Time{}, hearingStart.Add(1200*time.Millisecond))
	h.feedInTime(20 + 5 + 10)
	h.st.text(h.ctx, UtteranceText{Utterance: 0, Err: errors.New("speech-to-text is down")})
	h.feedInTime(6 + 7) // "Yes.", then its pause
	h.say(1, "Yes.", true)
	h.answer(t, true, nil)
	if got, ok := h.turn(); !ok || got.words != "Yes." {
		t.Errorf("took %+v (%v), want \"Yes.\" taken: the speech lost was passed over, not part of the turn", got, ok)
	}
}

func TestEveryTurnClosedAtTheMostIsTakenInOrderThoughTheFirstsTextComesLast(t *testing.T) {
	// Two utterances of speech each running past a second, a pause between them.
	h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 40), frames(0.1, 10), frames(0.9, 40))
	h.st.s.settings.Listening.MaxSpeech = time.Second
	h.feed(20 + 40 + 10 + 40) // both closed at the most
	h.say(1, "and then some", true)
	h.say(0, "I kept on talking", true)
	first, _ := h.turn()
	second, _ := h.turn()
	if first.words != "I kept on talking" || second.words != "and then some" || second.by != endedByMaxSpeech {
		t.Errorf("took %q, then %q (%s); want both closed turns, in the order spoken", first.words, second.words, second.by)
	}
}

func TestSpeechOverTheAgentReachesThePageAndIsTakenOnceHeld(t *testing.T) {
	h := newHearingHarness(t, frames(0.1, 20), frames(0.9, 20))
	run := h.speaking(t, time.Time{}, time.Time{})
	h.feed(20 + 6) // 6 frames of speech, 192 ms: not yet held
	h.say(0, "Sorry,", false)
	if _, cut := run.interruptedAt(); cut || len(h.page.messages) != 0 {
		t.Fatalf("before the interrupt speech: interrupted %v, the page told %q; want neither", cut, h.page.messages)
	}
	h.feed(1) // the 7th frame completes 0.2 s
	if _, cut := run.interruptedAt(); !cut {
		t.Fatal("not interrupted once the speech was held")
	}
	h.say(0, "Sorry, one thing", false)
	h.feed(13 + 7) // the pause
	h.say(0, "Sorry, one thing.", true)
	h.answer(t, true, nil)
	if got, ok := h.turn(); !ok || got.words != "Sorry, one thing." {
		t.Errorf("took %+v (%v), want the speech that cut in taken as the next turn, from its first word", got, ok)
	}
	want := []string{"Sorry,", "Sorry, one thing", "Sorry, one thing.", "Sorry, one thing. (final)"}
	if heard := h.heard(t); !slices.Equal(heard, want) {
		t.Errorf("the page heard %q, want %q: the words so far once held, then as they come", heard, want)
	}
}

func TestSpeakingAgainAfterATurnCappedAtTheMostIsANewTurnNotTalkingOn(t *testing.T) {
	h := newHearingHarness(t, speakingOnce...)
	run := &turnRun{request: uuid.NewV7(), interrupted: make(chan struct{}), stopSpeaking: func() {}, free: make(chan struct{})}
	h.replies.last = run
	h.st.taken = &spokenTurn{request: run.request, capped: true, texts: map[int]string{}, final: map[int]bool{}, lost: map[int]bool{}}
	h.feed(21) // the speech begins
	if h.st.turn == nil || h.st.turn.request == run.request || run.talkedOn() {
		t.Errorf("speaking again opened %+v and talked on: %v; want a new turn, the capped one's reply left", h.st.turn, run.talkedOn())
	}
}
