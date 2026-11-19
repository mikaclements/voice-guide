package speech

import "time"

// Listening sets how speech notices the user speaking, pausing and cutting in.
type Listening struct {
	// Threshold is the probability from which a frame counts as speech.
	Threshold float32
	// Pause is how long the user is quiet after speaking before speech asks
	// whether they have finished.
	Pause time.Duration
	// MaxWait is how long the user may stay quiet after speaking before their
	// turn is taken anyway.
	MaxWait time.Duration
	// InterruptSpeech is how much of the user's speech, counted in frames
	// judged speech within one utterance with no pause, counts as cutting in on
	// the agent.
	InterruptSpeech time.Duration
	// MaxSpeech is the most audio one turn's speech may run to, pauses
	// within it included, before it is taken with what was heard.
	MaxSpeech time.Duration
}

// noticing is what the listener noticed in the user's audio.
type noticing string

const (
	// noticedSpeech: the user started speaking, or spoke again after a pause.
	noticedSpeech noticing = "speech"
	// noticedPause: the user has been quiet for the pause since speaking.
	noticedPause noticing = "pause"
	// noticedWait: the user has stayed quiet for the maximum wait.
	noticedWait noticing = "waited"
	// noticedHeld: frames judged speech in one utterance with no pause add up
	// to the interrupt speech, enough to cut in should the agent's reply be
	// reaching the user; whether it does is the turn's to say.
	noticedHeld noticing = "held"
)

// event is something noticed, when the listener's clock said it was noticed,
// and the point in the connection's audio it marks, counted in samples from
// the first: where speech began, for speech; where it ended, for a pause and
// the maximum wait; where it reached the interrupt speech, for held speech.
type event struct {
	noticed noticing
	at      time.Time
	sample  int64
}

// recentAudio is how much of the user's latest audio the listener keeps: 8 s,
// the most the end-of-turn check reads; 256 KB a connection.
const recentAudio = 8 * ListenRate

// listener notices, in one connection's audio, the user starting to speak,
// pausing, staying quiet for the maximum wait, and speaking long enough to
// cut in. Every length is counted in the audio itself, frame by frame, so
// what is noticed doesn't depend on when the audio arrived; each event is
// timed by the clock. It keeps the latest audio for what comes after: the
// audio from just before speech began, and the end-of-turn check.
type listener struct {
	detector Detector
	settings Listening
	clock    clock

	frame  []float32 // the frame being filled, scaled to [-1, 1]
	judged int64     // samples judged so far, whole frames

	speaking bool  // speech began, and no pause since
	paused   bool  // a pause since speaking, the maximum wait not yet up
	held     bool  // this utterance has lasted the interrupt speech
	voiced   int64 // samples of this utterance judged speech, the quiet within it left out
	ended    int64 // where the last frame of speech ended

	recent []int16 // the latest audio, a ring of recentAudio samples
	heard  int64   // samples heard so far, judged or not
}

func newListener(d Detector, s Listening, c clock) *listener {
	return &listener{detector: d, settings: s, clock: c, frame: make([]float32, 0, DetectionFrame), recent: make([]int16, recentAudio)}
}

// hear takes the next stretch of the user's audio and returns what it
// noticed, judging every whole frame the audio so far makes. Audio that never
// came is silence: nothing of the user was heard in it. Whole frames of it
// are judged so without the detector, so missing audio costs next to
// nothing, however much a browser claims went missing.
func (l *listener) hear(a UserAudio) []event {
	var events []event
	missing := a.Missing
	for ; missing > 0 && len(l.frame) > 0; missing-- {
		events = l.add(0, events) // the frame begun ends in silence
	}
	for ; missing >= DetectionFrame; missing -= DetectionFrame {
		l.silence(DetectionFrame)
		events = l.judge(events, false)
	}
	for range missing {
		events = l.add(0, events)
	}
	for _, s := range a.PCM {
		events = l.add(s, events)
	}
	return events
}

// add takes one sample, judging the frame it completes.
func (l *listener) add(s int16, events []event) []event {
	l.recent[l.heard%recentAudio] = s
	l.heard++
	l.frame = append(l.frame, float32(s)/32768)
	if len(l.frame) < DetectionFrame {
		return events
	}
	events = l.judge(events, l.detector.Speech(l.frame) >= l.settings.Threshold)
	l.frame = l.frame[:0]
	return events
}

// silence keeps n samples of silence as the latest audio.
func (l *listener) silence(n int) {
	start := int(l.heard % recentAudio)
	clear(l.recent[start:min(start+n, recentAudio)])
	clear(l.recent[:max(start+n-recentAudio, 0)])
	l.heard += int64(n)
}

// judge adds to events what the next frame, judged speech or not, makes
// noticed.
func (l *listener) judge(events []event, speech bool) []event {
	start, end := l.judged, l.judged+DetectionFrame
	l.judged = end
	note := func(n noticing, at int64) { events = append(events, event{noticed: n, at: l.clock.Now(), sample: at}) }
	switch {
	case speech && !l.speaking:
		l.speaking, l.paused, l.held, l.voiced = true, false, false, 0
		note(noticedSpeech, start)
	case !speech && l.speaking && end-l.ended >= samples(l.settings.Pause):
		l.speaking, l.paused = false, true
		note(noticedPause, l.ended)
	case !speech && l.paused && end-l.ended >= samples(l.settings.MaxWait):
		l.paused = false
		note(noticedWait, l.ended)
	}
	if speech {
		l.ended = end
		// Quiet shorter than the pause is part of the utterance, as between words, but only frames
		// judged speech count towards holding it, so two taps a few frames apart don't add up to a
		// word, while a frame a word's quiet consonant loses doesn't start the count again.
		l.voiced += DetectionFrame
		if !l.held && l.voiced >= samples(l.settings.InterruptSpeech) {
			l.held = true
			note(noticedHeld, end)
		}
	}
	return events
}

// audio returns the audio heard from sample from on, as far back as the
// listener keeps it.
func (l *listener) audio(from int64) []int16 {
	from = min(max(from, l.heard-recentAudio, 0), l.heard)
	out := make([]int16, 0, l.heard-from)
	for i := from; i < l.heard; i++ {
		out = append(out, l.recent[i%recentAudio])
	}
	return out
}

// samples is d in samples of the user's audio.
func samples(d time.Duration) int64 { return int64(d * ListenRate / time.Second) }
