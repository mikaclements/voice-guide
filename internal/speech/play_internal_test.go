package speech

import (
	"context"
	"testing"
	"time"
)

// testClock moves only when play waits on it, as if each wait took exactly
// as long as asked.
type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

func (c *testClock) Until(ctx context.Context, t time.Time) bool {
	if t.After(c.now) {
		c.now = t
	}
	return ctx.Err() == nil
}

// track records when each packet was played and the silence before it, and
// can make time pass after a packet, as text-to-speech does between phrases;
// its round trip to the browser is roundTrip.
type track struct {
	clock     *testClock
	gapAfter  int
	gap       time.Duration
	afterPlay func(played int)
	roundTrip time.Duration
	at        []time.Duration // since the clock's start
	silences  []time.Duration
}

func (t *track) Play(_ []byte, silence time.Duration) error {
	t.at = append(t.at, t.clock.now.Sub(time.Time{}))
	t.silences = append(t.silences, silence)
	if len(t.at) == t.gapAfter {
		t.clock.now = t.clock.now.Add(t.gap)
	}
	if t.afterPlay != nil {
		t.afterPlay(len(t.at))
	}
	return nil
}

func (t *track) Done() <-chan struct{}    { return nil }
func (t *track) Close(CloseReason)        {}
func (t *track) Messages() <-chan []byte  { return nil }
func (t *track) Send([]byte) error        { return nil }
func (t *track) RoundTrip() time.Duration { return t.roundTrip }
func (t *track) Listen() <-chan UserAudio { return nil }

// offAir notes nothing of what plays.
func offAir(time.Time, bool) {}

func TestPacketsArePacedAndASilenceIsCountedNotMadeUp(t *testing.T) {
	clock := &testClock{}
	// The next phrase's audio arrives 100 ms after the second packet was played.
	tr := &track{clock: clock, gapAfter: 2, gap: 100 * time.Millisecond}
	packets := make(chan Packet, 4)
	for range 4 {
		packets <- Packet{Opus: []byte{0xf8, 0xff, 0xfe}}
	}
	close(packets)
	play(context.Background(), tr, packets, clock, time.Time{}, offAir)

	ms := time.Millisecond
	// One every 20 ms; after the gap the count starts again at once, with no burst to catch up.
	wantAt := []time.Duration{0, 20 * ms, 120 * ms, 140 * ms}
	// The second packet ended at 40 ms and the third played at 120 ms: 80 ms of silence.
	wantSilences := []time.Duration{0, 0, 80 * ms, 0}
	for i := range wantAt {
		if tr.at[i] != wantAt[i] || tr.silences[i] != wantSilences[i] {
			t.Errorf("packet %d played at %s after %s of silence, want at %s after %s", i, tr.at[i], tr.silences[i], wantAt[i], wantSilences[i])
		}
	}
}

func TestThePauseSinceTheReplyBeforeIsCountedAsSilence(t *testing.T) {
	ms := time.Millisecond
	for _, tc := range []struct {
		name        string
		due, now    time.Duration // since the clock's start; due 0 is no reply before
		wantAt      time.Duration
		wantSilence time.Duration
	}{
		{"seconds after the reply before", 40 * ms, 7040 * ms, 7040 * ms, 7000 * ms},
		{"the first reply", 0, 500 * ms, 500 * ms, 0},
		{"before the reply before's next packet was due", 40 * ms, 30 * ms, 40 * ms, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &testClock{now: time.Time{}.Add(tc.now)}
			tr := &track{clock: clock}
			packets := make(chan Packet, 1)
			packets <- Packet{Opus: []byte{0xf8, 0xff, 0xfe}}
			close(packets)
			var due time.Time
			if tc.due > 0 {
				due = time.Time{}.Add(tc.due)
			}
			play(context.Background(), tr, packets, clock, due, offAir)
			if tr.at[0] != tc.wantAt || tr.silences[0] != tc.wantSilence {
				t.Errorf("first packet played at %s after %s of silence, want at %s after %s", tr.at[0], tr.silences[0], tc.wantAt, tc.wantSilence)
			}
		})
	}
}

// readyClock's waits return as if their time had come even once ctx is done,
// as a wait whose time and cancellation come together may.
type readyClock struct{ testClock }

func (c *readyClock) Until(_ context.Context, t time.Time) bool {
	c.testClock.Until(context.Background(), t)
	return true
}

func TestNoPacketGoesOutOnceTheUserHasCutIn(t *testing.T) {
	clock := &readyClock{}
	ctx, cutIn := context.WithCancel(t.Context())
	tr := &track{clock: &clock.testClock, afterPlay: func(played int) {
		if played == 2 {
			cutIn()
		}
	}}
	packets := make(chan Packet, 4)
	for range 4 {
		packets <- Packet{Opus: []byte{0xf8, 0xff, 0xfe}}
	}
	close(packets)
	p := play(ctx, tr, packets, clock, time.Time{}, offAir)
	if len(tr.at) != 2 || len(p.sent) != 2 || p.stopped.IsZero() {
		t.Errorf("played %d packets, recorded %d, stopped at %v; want 2, 2 and the time it stopped", len(tr.at), len(p.sent), p.stopped)
	}
}

// recorder is a connection that keeps the messages sent to the browser.
type recorder struct {
	track
	messages []string
}

func (r *recorder) Send(msg []byte) error {
	r.messages = append(r.messages, string(msg))
	return nil
}

func TestAPieceOnItsWayAsTheUserCutInIsNeitherShownNorSaid(t *testing.T) {
	conn := &recorder{}
	run := &turnRun{interrupted: make(chan struct{}), stopSpeaking: func() {}, free: make(chan struct{})}
	text := make(chan string, 2)
	var firstText time.Time
	send := (&Service{clock: &testClock{}}).forward(t.Context(), conn, run, "a request", 0, text, &firstText)
	send("Tell me")
	run.interrupt(time.Time{})
	send(" about it.")
	if len(conn.messages) != 1 || len(text) != 1 {
		t.Errorf("the page got %q and text-to-speech %d pieces; want only the piece before the cut, once each", conn.messages, len(text))
	}
}

func newTestRun() *turnRun {
	return &turnRun{interrupted: make(chan struct{}), stopSpeaking: func() {}, free: make(chan struct{})}
}

func TestTalkingOnSettlingAndTheReplysFirstPacketExcludeEachOther(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	talked, told := newTestRun(), false
	if !talked.stopForTalkOn(now, 0, func() { told = true }) || !told {
		t.Fatal("talking on before any packet didn't stop the reply and tell the page")
	}
	if talked.sound(now) || talked.settle() {
		t.Error("a packet may go out, or the turn be settled, once the user talked on; want neither")
	}
	if talked.show(func() { t.Error("text told the page once the user talked on") }) {
		t.Error("show said the text went, want it held back")
	}
	settled := newTestRun()
	if !settled.settle() || settled.stopForTalkOn(now, 0, func() { t.Error("the page told of talking on after settling") }) {
		t.Error("talking on stopped a reply once its turn was settled, want it left to the interrupt")
	}
}

func TestTalkingOnEndsWhereSpeechOverTheAgentBegins(t *testing.T) {
	first, roundTrip := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC), 30*time.Millisecond
	// The first packet is heard 100 ms after it went: the round trip, the browser's delay and the
	// microphone's capture.
	for _, after := range []time.Duration{0, 50 * time.Millisecond, 99 * time.Millisecond, 100 * time.Millisecond, 101 * time.Millisecond, time.Second} {
		run := newTestRun()
		run.sound(first)
		var a aired
		a.note(run, first, true)
		began := first.Add(after)
		over := a.heard(began.Add(-captureDelay), roundTrip) != nil
		if talked := run.stopForTalkOn(began, roundTrip, func() {}); talked == over {
			t.Errorf("speech %s after the first packet: talking on %v, over the agent %v; want exactly one", after, talked, over)
		}
	}
}

// waited is a clock whose waits return only once the test lets them, saying what they wait for.
type waited struct {
	waits   chan time.Time
	release chan struct{}
}

func (c *waited) Now() time.Time { return time.Time{} }

func (c *waited) Until(ctx context.Context, t time.Time) bool {
	c.waits <- t
	select {
	case <-c.release:
		return true
	case <-ctx.Done():
		return false
	}
}

// Speech is noticed once the microphone packet holding the end of the frame it begins in arrives,
// up to a frame and a packet after it began: speech begun just before the reply's first packet is
// heard must still find the turn unsettled, and talk on.
func TestSpeechBegunJustBeforeTheReplyIsHeardTalksOnThoughNoticedLater(t *testing.T) {
	first, roundTrip := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC), 30*time.Millisecond
	clock := &waited{waits: make(chan time.Time, 1), release: make(chan struct{})}
	s := &Service{clock: clock}
	run := newTestRun()
	run.sound(first)
	settled := make(chan bool, 1)
	// The settle-by time bounds a context, which runs on the real clock, so it is set from the
	// real clock: a fixed date passes one day and settles the turn before the user can talk on.
	settleBy := time.Now().Add(time.Hour)
	go func() {
		settled <- s.reached(t.Context(), &track{roundTrip: roundTrip}, run, make(chan struct{}), settleBy)
	}()
	var until time.Time
	select {
	case until = <-clock.waits:
	case got := <-settled:
		t.Fatalf("settled (%v) at the first packet sent, want the turn held until it could be heard", got)
	}
	began := first.Add(roundTrip + browserDelay + captureDelay - time.Millisecond)
	if noticed := began.Add(audioTime(DetectionFrame) + PacketDuration); until.Before(noticed) {
		t.Errorf("held until %s after the first packet, want at least %s: until speech begun before it was heard is noticed", until.Sub(first), noticed.Sub(first))
	}
	if !run.stopForTalkOn(began, roundTrip, func() {}) {
		t.Error("speech begun before the reply was heard didn't talk on")
	}
	close(clock.release)
	if <-settled {
		t.Error("the turn was settled, want it taken back: the user talked on")
	}
}

// A reply whose first audio never comes is settled by its turn's deadline, so its turn ends in time.
func TestAReplyThatNeverSoundsIsSettledByTheTurnsDeadline(t *testing.T) {
	s := &Service{clock: systemClock{}}
	settled := make(chan bool, 1)
	go func() {
		settled <- s.reached(t.Context(), &track{}, newTestRun(), make(chan struct{}), time.Now())
	}()
	select {
	case got := <-settled:
		if !got {
			t.Error("not settled, want the turn saved as it ended")
		}
	case <-time.After(time.Second):
		t.Fatal("still waiting for the reply's first audio past the turn's deadline")
	}
}
