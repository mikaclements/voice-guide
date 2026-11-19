package speech

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// scripted judges each frame by its script, then as silence, and keeps the
// frames it was given.
type scripted struct {
	script []float32
	frames [][]float32
}

func (s *scripted) Speech(frame []float32) float32 {
	s.frames = append(s.frames, slices.Clone(frame))
	if len(s.frames) > len(s.script) {
		return 0
	}
	return s.script[len(s.frames)-1]
}

// listening is a set of settings with round numbers in frames of 512
// samples: a pause of 7 frames, a maximum wait of 94 and an interrupt of 7;
// a turn's speech runs to a minute.
var listening = Listening{Threshold: 0.5, Pause: 200 * time.Millisecond, MaxWait: 3 * time.Second, InterruptSpeech: 200 * time.Millisecond, MaxSpeech: time.Minute}

// frames repeats p n times: n frames judged p.
func frames(p float32, n int) []float32 { return slices.Repeat([]float32{p}, n) }

// hearFrames feeds the listener n frames of silence, which the script judges.
func hearFrames(l *listener, n int) []event {
	return l.hear(UserAudio{PCM: make([]int16, n*DetectionFrame)})
}

// noticed lists what was noticed, and at which sample.
func noticed(events []event) []string {
	var got []string
	for _, e := range events {
		got = append(got, fmt.Sprintf("%s@%d", e.noticed, e.sample))
	}
	return got
}

func newScriptedListener(script ...[]float32) (*listener, *testClock) {
	clock := &testClock{}
	return newListener(&scripted{script: slices.Concat(script...)}, listening, clock), clock
}

func TestSpeechStartsAtTheFirstFrameAtTheThreshold(t *testing.T) {
	l, _ := newScriptedListener([]float32{0.2, 0.49, 0.5, 0.9, 0.8})
	got := noticed(hearFrames(l, 5))
	if want := []string{"speech@1024"}; !slices.Equal(got, want) {
		t.Errorf("noticed %v, want %v: speech from the third frame's first sample, once", got, want)
	}
}

func TestAPauseIsNoticedOnceTheUserIsQuietForThePause(t *testing.T) {
	// Speech for 10 frames, to sample 5120, held in its seventh; 0.2 s is 3200 samples, reached in
	// the seventh quiet frame, which ends at 8704. The pause marks where speech ended.
	l, _ := newScriptedListener(frames(0.9, 10))
	if got, want := noticed(hearFrames(l, 16)), []string{"speech@0", "held@3584"}; !slices.Equal(got, want) {
		t.Errorf("noticed %v in six quiet frames, want %v: 3072 samples are no pause", got, want)
	}
	if got, want := noticed(hearFrames(l, 4)), []string{"pause@5120"}; !slices.Equal(got, want) {
		t.Errorf("noticed %v from the seventh quiet frame on, want %v, once", got, want)
	}
}

func TestQuietShorterThanThePauseIsPartOfTheSpeech(t *testing.T) {
	// 4 frames of speech, 6 quiet, then speech again: held in the third frame after the quiet, the
	// seventh judged speech in the utterance, so the quiet didn't start a utterance of its own.
	l, _ := newScriptedListener(frames(0.9, 4), frames(0.1, 6), frames(0.9, 3))
	got := noticed(hearFrames(l, 13))
	if want := []string{"speech@0", "held@6656"}; !slices.Equal(got, want) {
		t.Errorf("noticed %v, want %v: six quiet frames, 3072 samples, are no pause", got, want)
	}
}

func TestSpeechAfterAPauseStartsAgain(t *testing.T) {
	l, _ := newScriptedListener(frames(0.9, 5), frames(0.1, 10), frames(0.9, 2))
	got := noticed(hearFrames(l, 17))
	if want := []string{"speech@0", "pause@2560", "speech@7680"}; !slices.Equal(got, want) {
		t.Errorf("noticed %v, want %v", got, want)
	}
}

func TestTheMaximumWaitIsNoticedOnceAfterThePause(t *testing.T) {
	// 3 s is 48000 samples after speech ended at 2560: the frame ending at
	// 50688 reaches it.
	l, _ := newScriptedListener(frames(0.9, 5))
	if got, want := noticed(hearFrames(l, 98)), []string{"speech@0", "pause@2560"}; !slices.Equal(got, want) {
		t.Errorf("noticed %v up to the frame ending at 50176, want %v: the wait isn't up", got, want)
	}
	if got, want := noticed(hearFrames(l, 100)), []string{"waited@2560"}; !slices.Equal(got, want) {
		t.Errorf("noticed %v from the frame ending at 50688 on, want %v, once", got, want)
	}
}

func TestSpeechIsHeldOnceAnUtteranceLastsTheInterruptSpeech(t *testing.T) {
	// 0.2 s is 3200 samples from speech's start: the frame ending at 3584. The
	// second utterance, 6 frames or 3072 samples, is a click.
	l, _ := newScriptedListener(frames(0.9, 40), frames(0.1, 7), frames(0.9, 6))
	got := noticed(hearFrames(l, 70))
	want := []string{"speech@0", "held@3584", "pause@20480", "speech@24064", "pause@27136"}
	if !slices.Equal(got, want) {
		t.Errorf("noticed %v, want %v", got, want)
	}
}

func TestOnlyFramesJudgedSpeechCountTowardsTheInterruptSpeech(t *testing.T) {
	for name, tc := range map[string]struct {
		script [][]float32
		want   []string
	}{
		// Two taps 192 ms apart: the utterance spans 0.2 s, but only two of its frames were speech.
		"two speech frames with quiet between": {
			[][]float32{frames(0.9, 1), frames(0.1, 5), frames(0.9, 1)}, []string{"speech@0", "pause@3584"},
		},
		// A word the detector loses for a frame: the seventh frame judged speech holds it.
		"seven speech frames with a quiet one among them": {
			[][]float32{frames(0.9, 3), frames(0.1, 1), frames(0.9, 4)}, []string{"speech@0", "held@4096", "pause@4096"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			l, _ := newScriptedListener(tc.script...)
			if got := noticed(hearFrames(l, 20)); !slices.Equal(got, tc.want) {
				t.Errorf("noticed %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWhatIsNoticedIsTimedByTheClock(t *testing.T) {
	l, clock := newScriptedListener(frames(0.9, 1))
	clock.now = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	started := hearFrames(l, 1)
	clock.now = clock.now.Add(time.Second)
	paused := hearFrames(l, 7)
	if len(started) != 1 || !started[0].at.Equal(time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("speech noticed at %v, want the clock's time as its frame was judged", started)
	}
	if len(paused) != 1 || !paused[0].at.Equal(clock.now) {
		t.Errorf("pause noticed at %v, want %v", paused, clock.now)
	}
}

func TestAudioIsJudgedInWholeFramesScaledToOneWhateverSizeItComesIn(t *testing.T) {
	d := &scripted{}
	l := newListener(d, listening, &testClock{})
	// Eight 20 ms pieces, 2560 samples: five whole frames.
	for piece := range 8 {
		pcm := make([]int16, 320)
		for i := range pcm {
			pcm[i] = int16(piece*320 + i)
		}
		l.hear(UserAudio{PCM: pcm})
	}
	if len(d.frames) != 5 {
		t.Fatalf("judged %d frames, want 5", len(d.frames))
	}
	for f, frame := range d.frames {
		for i, s := range frame {
			if want := float32(f*DetectionFrame+i) / 32768; len(frame) != DetectionFrame || s != want {
				t.Fatalf("frame %d sample %d = %v, want %v: the audio in order, over 32768", f, i, s, want)
			}
		}
	}
}

func TestTheLastEightSecondsOfAudioAreKept(t *testing.T) {
	l := newListener(&scripted{}, listening, &testClock{})
	// Ten seconds, each millisecond's samples its number: no two seconds alike.
	at := func(sample int) int16 { return int16(sample / (ListenRate / 1000)) }
	for s := range 10 {
		second := make([]int16, ListenRate)
		for i := range second {
			second[i] = at(s*ListenRate + i)
		}
		l.hear(UserAudio{PCM: second})
	}
	kept := l.audio(0)
	if len(kept) != 8*ListenRate {
		t.Fatalf("kept %d samples, want the last 8 s, 128000", len(kept))
	}
	for i, v := range kept {
		if want := at(2*ListenRate + i); v != want {
			t.Fatalf("kept sample %d is %d, want %d: the last 8 s, in order", i, v, want)
		}
	}
	if last := l.audio(10*ListenRate - 4800); len(last) != 4800 || last[0] != at(10*ListenRate-4800) {
		t.Errorf("from 0.3 s before the end, got %d samples, want 4800 from millisecond 9700", len(last))
	}
	if ahead := l.audio(11 * ListenRate); len(ahead) != 0 {
		t.Errorf("from past the end, got %d samples, want none", len(ahead))
	}
}

func TestWholeFramesOfMissingAudioAreSilenceTheDetectorNeverJudges(t *testing.T) {
	// Half a frame heard, then 10 s and 100 samples missing, then 412 heard:
	// the frame begun ends in missing audio and the last mixes missing and
	// heard, so the detector judges those two; the 312 frames between are
	// silence. The pause and the maximum wait are still noticed in them.
	d := &scripted{script: frames(0.9, 1)}
	l := newListener(d, listening, &testClock{})
	got := noticed(l.hear(UserAudio{PCM: make([]int16, 256)}))
	got = append(got, noticed(l.hear(UserAudio{Missing: 10*ListenRate + 100, PCM: make([]int16, 412)}))...)
	if want := []string{"speech@0", "pause@512", "waited@512"}; !slices.Equal(got, want) {
		t.Errorf("noticed %v, want %v", got, want)
	}
	if len(d.frames) != 2 {
		t.Errorf("the detector judged %d frames, want 2: none wholly missing", len(d.frames))
	}
}

func TestAudioThatNeverCameCountsAsQuiet(t *testing.T) {
	// Speech for 10 frames, then 0.25 s that never came, judged as silence,
	// in which the pause is reached.
	l, _ := newScriptedListener(frames(0.9, 10))
	got := noticed(hearFrames(l, 10))
	got = append(got, noticed(l.hear(UserAudio{Missing: 4000, PCM: make([]int16, DetectionFrame)}))...)
	if want := []string{"speech@0", "held@3584", "pause@5120"}; !slices.Equal(got, want) {
		t.Errorf("noticed %v, want %v", got, want)
	}
	if kept := l.audio(5120); len(kept) != 4000+DetectionFrame || kept[0] != 0 {
		t.Errorf("kept %d samples since speech ended, want the 4000 missing as silence and the frame after", len(kept))
	}
}
