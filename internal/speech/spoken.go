package speech

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"
)

// prefixAudio is how much audio from before speech began goes to speech-to-text with it, unless
// an utterance heard before already sent it: 0.3 s. Speech detection judges a word's quiet start, a
// breath or a soft consonant, as silence for a frame or two, and speech-to-text needs it to hear
// the word; 0.3 s holds that with room, for a little silence more to recognise. A round guess,
// measured once spoken turns' transcripts are compared with what was said: first words lost
// mean longer, none lost at a shorter one means shorter.
const prefixAudio = 300 * time.Millisecond

// sttAhead bounds the pieces of the user's audio waiting for speech-to-text to take them: 256,
// about 5 s of the 20 ms stretches the transport yields. Speech-to-text takes each in
// milliseconds and gives up on a stuck service within a few seconds, so a full queue means it is
// stuck; an utterance that doesn't fit is dropped rather than held.
const sttAhead = 256

// errBehind loses an utterance speech-to-text was too far behind to take.
var errBehind = errors.New("speech-to-text is too far behind")

// How a spoken turn's end was decided, for its stage times: the end-of-turn check said the user
// had finished, the maximum wait was up, or the turn's speech ran to the most it may.
const (
	endedByCheck     = "check"
	endedByWait      = "wait"
	endedByMaxSpeech = "max_speech"
)

// heardTurn is a turn the user spoke, handed over to be taken: its request ID, made by speech,
// its words, and when its stages came, for its stage times.
type heardTurn struct {
	request uuid.UUID
	words   string
	// ended is when the user's speech ended, placed by the audio; finalText when its last final
	// text came; checked when the end-of-turn check answered about the last pause, zero if it
	// didn't in time; by is what ended the turn.
	ended, finalText, checked time.Time
	by                        string
	// talkedOn counts the times the user talked on after it was taken, and continues is the turn
	// begun with its words before they last did, which it takes the place of.
	talkedOn  int
	continues *turnRun
}

// spokenTurns makes turns of what the user says on one audio connection: it notices their
// speech, streams it to speech-to-text, asks the end-of-turn check at each pause, tells the page
// what it hears, cuts in on the agent when the user speaks over it, and hands each finished turn
// over to be taken. It runs on the goroutine that listens to the connection's microphone.
type spokenTurns struct {
	s       *Service
	conn    Connection
	session uuid.UUID
	replies *replies
	begin   func(context.Context, heardTurn) // begins a turn before it returns: replies.beginSpoken
	l       *listener

	// audio goes to speech-to-text and texts come back, from the user's first speech on; next is
	// the number the next utterance gets, and sent how far the open one's audio has gone.
	audio chan UtteranceAudio
	texts <-chan UtteranceText
	next  int
	sent  int64
	// backlog holds, in order, the ends and drops that found the queue full until speech-to-text
	// takes them: none is ever lost, since speech-to-text numbers utterances by them. An end keeps
	// the last of its utterance's audio, a frame or so, so an utterance whose audio all got through
	// is still heard. Only these wait there, one an utterance, so it grows no faster than the user
	// speaks, and only while speech-to-text is stuck. dropping says the open utterance didn't fit
	// and was dropped.
	backlog  []UtteranceAudio
	dropping bool

	// turn is the turn being spoken; closed, oldest first, those whose speech reached the most a
	// turn's may, each waiting for its final texts.
	turn   *spokenTurn
	closed []*spokenTurn
	// taken is the turn last handed over, which the user talks on after if they speak again before
	// its reply reaches them.
	taken *spokenTurn
	// asked numbers the end-of-turn checks, whose answers come on checks; cancel stops the check
	// in flight, if any, and checking waits for every one.
	asked    int
	checks   chan checkAnswer
	cancel   context.CancelFunc
	checking sync.WaitGroup
}

// spokenTurn is the turn the user is speaking: one utterance or more, each ended at a pause.
type spokenTurn struct {
	request    uuid.UUID
	start      int64          // where its audio sent to speech-to-text begins
	undecided  bool           // begun as the agent's reply reached the user, not yet held: passed over if it pauses first
	held       bool           // an utterance of its speech lasted the interrupt speech
	capped     bool           // its speech ran to the most one turn's may
	utterances []int          // in the order spoken
	texts      map[int]string // rough text, then final text, by utterance
	final      map[int]bool   // utterances whose final text came, or that failed
	waiting    int            // utterances without final text yet
	speaking   bool           // an utterance is open
	check      int            // the check that counts, asked at the latest pause; 0 while speaking
	finished   bool           // that check said the user has finished
	waited     bool           // the maximum wait is up
	lost       map[int]bool   // utterances speech-to-text couldn't hear in full
	told       string         // the words the page was last told, if any
	talkedOn   int            // how often the user talked on after it was handed over
	continues  *turnRun       // the turn begun with its words before the user last talked on
	ended      time.Time
	finalText  time.Time
	checked    time.Time
}

// checkAnswer is the end-of-turn check's answer to check n.
type checkAnswer struct {
	n        int
	finished bool
	err      error
}

func newSpokenTurns(s *Service, conn Connection, session uuid.UUID, r *replies) *spokenTurns {
	return &spokenTurns{s: s, conn: conn, session: session, replies: r, begin: r.beginSpoken, checks: make(chan checkAnswer)}
}

// listen hears the connection's microphone until ctx ends, handing each turn the user speaks to r,
// which begins it, and cutting in on the reply reaching the user when they speak over it. What it
// notices is logged too, where in the audio, never the audio.
func (s *Service) listen(ctx context.Context, conn Connection, session uuid.UUID, r *replies) {
	st := newSpokenTurns(s, conn, session, r)
	defer st.stop()
	for {
		// The oldest end or drop held back goes as soon as speech-to-text has room.
		var behind chan<- UtteranceAudio
		var head UtteranceAudio
		if len(st.backlog) > 0 {
			behind, head = st.audio, st.backlog[0]
		}
		select {
		case <-ctx.Done():
			return
		case behind <- head:
			st.backlog = st.backlog[1:]
		case a := <-conn.Listen():
			st.hear(ctx, a)
		case t, ok := <-st.texts:
			if !ok {
				st.texts = nil // ended with ctx
				continue
			}
			st.text(ctx, t)
		case c := <-st.checks:
			st.receiveCheck(ctx, c)
		}
	}
}

// stop ends the check in flight and the speech-to-text stream, and waits for both. ctx has ended.
func (st *spokenTurns) stop() {
	st.stopCheck()
	st.checking.Wait()
	if st.audio != nil {
		close(st.audio)
		for st.texts != nil {
			if _, ok := <-st.texts; !ok {
				st.texts = nil
			}
		}
	}
}

// hear takes the next stretch of the user's audio, acts on what it notices, and sends the open
// utterance's audio on to speech-to-text.
func (st *spokenTurns) hear(ctx context.Context, a UserAudio) {
	// Made with the first audio, so a connection whose browser sends no microphone holds none of
	// the listener's 256 KB.
	if st.l == nil {
		st.l = newListener(st.s.detection.Detector(), st.s.settings.Listening, st.s.clock)
	}
	for _, e := range st.l.hear(a) {
		st.s.log.Info("speech noticed", "session", st.session, "noticed", string(e.noticed), "audio_ms", e.sample*1000/ListenRate)
		switch e.noticed {
		case noticedSpeech:
			st.speech(ctx, e)
		case noticedPause:
			st.pause(ctx, e)
		case noticedWait:
			st.wait(ctx)
		case noticedHeld:
			st.interruptOnHeld(e)
		}
	}
	t := st.turn
	switch {
	case t == nil || !t.speaking:
	case st.l.heard >= t.start+samples(st.s.settings.Listening.MaxSpeech):
		st.most(ctx, t.start+samples(st.s.settings.Listening.MaxSpeech))
	default:
		st.put(UtteranceAudio{Samples: st.rest()})
	}
}

// overAgent says whether the agent's reply was reaching the user when they said what reached voice
// at at.
func (st *spokenTurns) overAgent(at time.Time) bool {
	return st.replies.aired.heard(at.Add(-captureDelay), st.conn.RoundTrip()) != nil
}

// speech opens an utterance, in the turn being spoken or a new one, and the speech-to-text stream
// with the first. Its audio starts prefixAudio before the speech did, or where the utterance
// before stopped sending, if later, so no audio of an utterance heard goes twice. After a blip
// passed over, that is where the blip's speech ended: the quiet after it went only with an
// utterance dropped, which speech-to-text has let go of, so it may go again.
func (st *spokenTurns) speech(ctx context.Context, e event) {
	st.replies.userActive()
	if st.audio == nil {
		st.audio = make(chan UtteranceAudio, sttAhead)
		st.texts = st.s.stt.Transcribe(ctx, st.audio)
	}
	// Speech beginning while the agent's reply reaches the user may be no more than a click or a
	// breath: such a turn counts once held. Before the reply's first audio, it is a turn.
	began := e.at.Add(-audioTime(st.l.heard - e.sample))
	over := st.overAgent(began)
	st.sent, st.dropping = max(e.sample-samples(prefixAudio), st.sent), false
	if st.turn == nil && !over {
		st.turn = st.reopenTaken(e, began)
	}
	st.taken = nil
	if st.turn == nil {
		st.turn = &spokenTurn{request: uuid.NewV7(), start: st.sent, undecided: over, texts: map[int]string{}, final: map[int]bool{}, lost: map[int]bool{}}
	}
	t := st.turn
	// Speaking again continues the turn: the check about the pause before no longer counts.
	st.stopCheck()
	t.speaking, t.check, t.finished, t.waited = true, 0, false, false
	t.utterances = append(t.utterances, st.next)
	t.waiting++
}

// pause ends the open utterance, asking for its final text, and asks the end-of-turn check about
// the latest audio at the same time. Speech over the agent throughout that paused before it was held
// is passed over instead.
func (st *spokenTurns) pause(ctx context.Context, e event) {
	t := st.turn
	if t == nil || !t.speaking {
		return
	}
	// The speech ended where the pause began, that much audio before the latest.
	ended := e.at.Add(-audioTime(st.l.heard - e.sample))
	if t.undecided && st.overAgent(ended) {
		st.passOver(e.sample)
		return
	}
	t.speaking = false
	// A dropped utterance has had its drop, which ended it for speech-to-text; any other ends now,
	// or once speech-to-text has room.
	if rest := st.rest(); !st.dropping {
		st.sendOrBacklog(UtteranceAudio{Samples: rest, End: true})
	}
	st.next++
	if t.undecided {
		// The reply stopped reaching the user during it, as when "Yes." overlaps its last syllable:
		// the user's turn.
		t.undecided = false
		st.tellWords(t)
	}
	t.ended = ended
	if t.failed() {
		st.handOverWhenReady(ctx, t) // nothing to ask: it can't be taken
		return
	}
	st.asked++
	t.check = st.asked
	st.ask(ctx, t.check, st.l.audio(st.l.heard-samples(TurnAudio)))
}

// failed says whether speech-to-text couldn't hear one of the turn's utterances in full.
func (t *spokenTurn) failed() bool { return len(t.lost) > 0 }

// passOver lets go of speech over the agent that never cut in, a click or a cough too short to be
// held, its speech ending at end: it isn't a turn of its own, so the model never answers it, and
// the page, never told of it, isn't told now. Its utterance, still open, is dropped through the
// swap point, so speech-to-text works out no final text for it ahead of what the user says next.
func (st *spokenTurns) passOver(end int64) {
	t := st.turn
	if !st.dropping {
		st.sendOrBacklog(UtteranceAudio{Drop: true})
	}
	st.next++
	st.turn = nil
	// What went after end went with the dropped utterance, so speech right after may still open
	// with it, as speech does with the audio before it; the blip itself never goes again.
	st.sent = end
	st.s.log.Info("speech over the agent passed over", "session", st.session, "utterances", len(t.utterances))
}

// interruptOnHeld runs when the turn being spoken has been held: its speech lasted the interrupt
// speech. The turn is now one to hand over, however it began. If the agent's reply is reaching
// the user, it stops at once and is saved as far as it was heard, before this turn is handed over
// with the rest of what the user says. The page was not told of speech over the agent until now,
// so it gets the words heard so far.
func (st *spokenTurns) interruptOnHeld(e event) {
	t := st.turn
	if t == nil || !t.speaking {
		return
	}
	t.held, t.undecided = true, false
	// The interrupt is when the held speech was said, so the reply counts as heard up to then.
	said := e.at.Add(-captureDelay)
	if run := st.replies.aired.heard(said, st.conn.RoundTrip()); run != nil {
		run.interrupt(said)
	}
	st.tellWords(t)
}

// most ends the turn's speech at end, the most one turn's may run to: the turn is closed, taken
// with what was heard up to there once its final texts are in, without the end-of-turn check. The
// rest of that speech isn't heard; speaking again after a pause is a new turn, even
// before the closed one's texts are in. The most counts from the turn's first audio sent, so no
// utterance holds more than that. Speech over the agent that never cut in is passed over instead.
func (st *spokenTurns) most(ctx context.Context, end int64) {
	t := st.turn
	if t.undecided {
		st.passOver(st.l.heard) // the rest of the speech until the pause isn't heard, as for a turn closed there
		return
	}
	rest := st.rest()
	rest = rest[:max(len(rest)-int(st.l.heard-end), 0)]
	t.speaking, t.capped = false, true
	if !st.dropping {
		st.sendOrBacklog(UtteranceAudio{Samples: rest, End: true})
	}
	st.next++
	t.ended = st.s.clock.Now().Add(-audioTime(st.l.heard - end))
	st.turn, st.closed = nil, append(st.closed, t)
	st.handOverWhenReady(ctx, t)
}

// wait takes the turn once the user has stayed quiet for the maximum wait, whatever the check
// said, or if it said nothing.
func (st *spokenTurns) wait(ctx context.Context) {
	if t := st.turn; t != nil && !t.speaking {
		t.waited = true
		st.handOverWhenReady(ctx, t)
	}
}

// rest returns the open utterance's audio not yet sent.
func (st *spokenTurns) rest() []int16 {
	a := st.l.audio(st.sent)
	st.sent = st.l.heard
	return a
}

// put queues a piece of the open utterance for speech-to-text, behind any end or drop held back.
// When it doesn't fit, speech-to-text is stuck: the utterance is dropped, through the port, so
// both sides still number the next one alike, and its words are lost.
func (st *spokenTurns) put(a UtteranceAudio) {
	if st.dropping || st.send(a) {
		return
	}
	st.dropping = true
	st.sendOrBacklog(UtteranceAudio{Drop: true})
	st.lose(st.next, errBehind)
}

// sendOrBacklog queues an end or a drop for speech-to-text, holding it back until there is room.
func (st *spokenTurns) sendOrBacklog(a UtteranceAudio) {
	if !st.send(a) {
		st.backlog = append(st.backlog, a)
	}
}

// send queues a behind what is held back, if it fits.
func (st *spokenTurns) send(a UtteranceAudio) bool {
	st.flush()
	// Speech-to-text may take a piece between the flush and here: what is held still goes first.
	if len(st.backlog) > 0 {
		return false
	}
	select {
	case st.audio <- a:
		return true
	default:
		return false
	}
}

// flush sends what is held back while speech-to-text has room.
func (st *spokenTurns) flush() {
	for len(st.backlog) > 0 {
		select {
		case st.audio <- st.backlog[0]:
			st.backlog = st.backlog[1:]
		default:
			return
		}
	}
}

// ask asks the end-of-turn check about audio as check n, stopping the check before.
func (st *spokenTurns) ask(ctx context.Context, n int, audio []int16) {
	st.stopCheck()
	ctx, cancel := context.WithCancel(ctx)
	st.cancel = cancel
	st.checking.Go(func() {
		finished, err := st.s.endOfTurn.Finished(ctx, audio)
		select {
		case st.checks <- checkAnswer{n: n, finished: finished, err: err}:
		case <-ctx.Done():
		}
	})
}

func (st *spokenTurns) stopCheck() {
	if st.cancel != nil {
		st.cancel()
		st.cancel = nil
	}
}

// receiveCheck takes the end-of-turn check's answer, unless the user has spoken since it was asked
// or the turn was taken without it.
func (st *spokenTurns) receiveCheck(ctx context.Context, c checkAnswer) {
	t := st.turn
	if t == nil || c.n != t.check {
		return
	}
	if c.err != nil {
		// The maximum wait decides instead.
		st.s.log.Warn("ask whether the user has finished", "session", st.session, "err", c.err)
		return
	}
	t.checked, t.finished = st.s.clock.Now(), c.finished
	st.handOverWhenReady(ctx, t)
}

// text takes what speech-to-text heard of one of a turn's utterances, and tells the page.
func (st *spokenTurns) text(ctx context.Context, u UtteranceText) {
	t := st.owner(u.Utterance)
	if t == nil || t.final[u.Utterance] {
		return
	}
	switch {
	case u.Err != nil:
		st.lose(u.Utterance, u.Err)
	case u.Final:
		t.texts[u.Utterance], t.final[u.Utterance] = u.Text, true
		t.waiting--
		t.finalText = st.s.clock.Now()
	default:
		t.texts[u.Utterance] = u.Text
	}
	st.tellWords(t)
	st.handOverWhenReady(ctx, t)
}

// owner returns the turn utterance n belongs to, if any.
func (st *spokenTurns) owner(n int) *spokenTurn {
	for _, t := range append([]*spokenTurn{st.turn}, st.closed...) {
		if t != nil && slices.Contains(t.utterances, n) {
			return t
		}
	}
	return nil
}

// tellWords tells the page the words heard of the turn when they have changed, unless it is speech
// over the agent not yet held, which may yet be passed over.
func (st *spokenTurns) tellWords(t *spokenTurn) {
	if words := t.words(); words != "" && words != t.told && !t.undecided {
		t.told = words
		st.s.tell(st.conn, out{Type: "hearing", RequestID: t.request.String(), Text: words})
	}
}

// lose gives up on utterance n, which speech-to-text couldn't hear in full, keeping its rough text:
// its turn can't be taken without its words.
func (st *spokenTurns) lose(n int, err error) {
	if t := st.owner(n); t != nil && !t.final[n] {
		st.s.log.Warn("hear what the user said", "session", st.session, "err", err)
		t.final[n], t.lost[n] = true, true
		t.waiting--
	}
}

// handOverWhenReady hands turn t over once every utterance has its final text and either the check said the
// user has finished, the maximum wait is up, or its speech ran to the most a turn's may; a turn
// closed at that most goes before the one spoken after it. Speech over the agent not yet held is
// never taken.
func (st *spokenTurns) handOverWhenReady(ctx context.Context, t *spokenTurn) {
	if t == nil || t.undecided || t.speaking || t.waiting > 0 || !t.failed() && !t.finished && !t.waited && !t.capped {
		return
	}
	if i := slices.Index(st.closed, t); i >= 0 {
		if i > 0 {
			return
		}
		st.closed = st.closed[1:]
		st.handOver(ctx, t)
		// The turn spoken after it may be ready already.
		if len(st.closed) > 0 {
			st.handOverWhenReady(ctx, st.closed[0])
		} else {
			st.handOverWhenReady(ctx, st.turn)
		}
		return
	}
	if len(st.closed) > 0 {
		return
	}
	st.turn = nil
	st.stopCheck()
	st.handOver(ctx, t)
}

// reopenTaken reopens the turn last handed over when the user, beginning to speak at began, talks on
// before its reply has reached them: the reply stops, the turn is taken back, and the page is told,
// then shown the turn's words again, which what the user says now joins. A turn whose speech ran to
// the most one may hold isn't reopened: speaking again after it is a new turn.
func (st *spokenTurns) reopenTaken(e event, began time.Time) *spokenTurn {
	t := st.taken
	if t == nil || t.capped {
		return nil
	}
	run := st.replies.begun(t.request)
	if run == nil || !run.stopForTalkOn(began, st.conn.RoundTrip(), func() {
		st.s.tell(st.conn, out{Type: "talking_on", RequestID: t.request.String()})
	}) {
		return nil
	}
	st.s.log.Info("talking on", "session", st.session, "audio_ms", e.sample*1000/ListenRate)
	t.talkedOn, t.continues = t.talkedOn+1, run
	t.told, t.checked = "", time.Time{}
	st.tellWords(t)
	return t
}

// handOver tells the page the words turn t is taken with and hands it over. A turn with none, as when
// only a cough was heard, isn't taken, and a page shown rough text is told so. A turn
// speech-to-text couldn't hear in full isn't taken either, and ends as soon as the user pauses: the
// page gets the words heard so far, for the user to send or say again.
func (st *spokenTurns) handOver(ctx context.Context, t *spokenTurn) {
	words := t.words()
	if t.failed() {
		st.s.tell(st.conn, out{Type: "hearing", RequestID: t.request.String(), Text: words, Final: true, Failed: true})
		return
	}
	if words != "" || t.told != "" {
		st.s.tell(st.conn, out{Type: "hearing", RequestID: t.request.String(), Text: words, Final: true})
	}
	if words == "" {
		return
	}
	by := endedByWait
	switch {
	case t.capped:
		by = endedByMaxSpeech
	case t.finished:
		by = endedByCheck
	}
	st.begin(ctx, heardTurn{request: t.request, words: words, ended: t.ended, finalText: t.finalText, checked: t.checked, by: by, talkedOn: t.talkedOn, continues: t.continues})
	st.taken = t
}

// words are the texts of the turn's utterances, in order, joined.
func (t *spokenTurn) words() string {
	var words []string
	for _, n := range t.utterances {
		if w := strings.TrimSpace(t.texts[n]); w != "" {
			words = append(words, w)
		}
	}
	return strings.Join(words, " ")
}

// audioTime is how long n samples of the user's audio last.
func audioTime(n int64) time.Duration { return time.Duration(n) * time.Second / ListenRate }
