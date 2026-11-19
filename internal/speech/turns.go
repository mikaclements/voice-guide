package speech

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/conversation"
)

// textAhead bounds the reply's text taken but not yet spoken. A reply comes a
// few words at a time, so a long one is a few hundred pieces, and
// text-to-speech reads them as they come: the text never waits on the audio.
const textAhead = 1024

// waitingTurns bounds the turns waiting behind the one being taken on a
// connection; more are refused as busy. A person can't type faster than an
// interrupted turn is saved, about 15 ms measured, so more than a couple
// waiting means a script or a stuck key: a round guess.
const waitingTurns = 2

// serve takes the turns the browser types on its data channel and those the
// user speaks into its microphone, in order, until the connection ends; then it
// cancels the turn in flight, waits for it to be saved, and lets the
// connection go. A turn that arrives while the agent answers or speaks
// interrupts it: the user cut in.
func (s *Service) serve(c access.Caller, session, id uuid.UUID, conn Connection, hangup <-chan struct{}, served chan<- struct{}) {
	defer s.watchers.Done()
	defer close(served) // after the turns are saved and the connection let go, below
	defer s.letGo(session, id)
	ctx, cancel := context.WithCancel(context.Background())
	var turns sync.WaitGroup
	defer turns.Wait()
	defer cancel()
	r := &replies{s: s, conn: conn, caller: c, session: session, turns: &turns, waiting: make(chan struct{}, waitingTurns), active: make(chan struct{}, 1)}
	// The microphone is listened to for as long as the connection lasts, and the session is marked
	// active as the user speaks.
	turns.Go(func() { s.listen(ctx, conn, session, r) })
	turns.Go(func() { s.keepActive(ctx, c, session, r.active) })
	// A connection opened with a ticket can't outlive it, so a stolen ticket's
	// connection stops at the ticket's lifetime too.
	due := make(chan struct{})
	expired := due
	turns.Go(func() {
		if s.clock.Until(ctx, c.Expires) {
			close(due)
		}
	})
	// The audio stops when the ticket expires, so the reply reaching the user is cut there too,
	// and the turns stop before the connection closes, which can take a while: none may be taken
	// meanwhile, typed or spoken.
	expire := func() {
		expired = nil
		r.interruptLast(s.clock.Now())
		cancel()
		conn.Close(Expired)
	}
	for {
		select {
		case <-conn.Done():
			return
		case <-hangup:
			// The caller ends this connection, as the page does when it ends the session: the
			// reply reaching the user is cut where they stopped hearing it, as an interrupt is,
			// rather than saved whole or lost as when a connection drops.
			r.interruptLast(s.clock.Now())
			conn.Close(Ended)
		case <-expired:
			expire()
		case raw := <-conn.Messages():
			received := s.clock.Now()
			// A turn read once the connection has ended, or once the ticket has expired, as
			// when one waited as the cap passed, is never taken.
			if ended(conn) {
				continue
			}
			if c.ExpiredAt(received) {
				expire()
				continue
			}
			var m turnIn
			if err := json.Unmarshal(raw, &m); err != nil || m.Type != "turn" || m.RequestID == uuid.Nil() {
				s.tell(conn, out{Type: "refused", RequestID: requestOf(m), Reason: reasonUnreadable,
					Detail: `a turn is a JSON object of type "turn" with request_id, a UUID, the words, and an optional screen_input list of name and content`})
				continue
			}
			r.begin(ctx, m, received, nil)
		}
	}
}

// replies takes one connection's turns, typed and spoken, in order, and keeps
// what the agent's replies have sounded like to the user. A turn begins under
// its lock on the goroutine that read it, the data channel's or the
// microphone's, so a spoken turn has begun by the time it is handed over.
type replies struct {
	s       *Service
	conn    Connection
	caller  access.Caller
	session uuid.UUID
	turns   *sync.WaitGroup
	waiting chan struct{} // a place for each turn waiting
	active  chan struct{} // the user began speaking, for marking the session active
	aired   aired

	mu   sync.Mutex
	last *turnRun // the turn the next one interrupts, or waits for
}

// begin takes the turn m once the one before it is saved, interrupting that
// one, or refuses it when too many wait.
func (r *replies) begin(ctx context.Context, m turnIn, received time.Time, said *heardTurn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Once the ticket has expired no turn is taken, whichever way it came: the connection is
	// closing for it.
	if r.caller.ExpiredAt(r.s.clock.Now()) {
		return
	}
	before := r.last
	wait := before != nil && !before.released()
	// The same request again while its turn is answered is the page
	// retrying, not the user cutting in on their own turn: it waits,
	// and conversation says whether it was taken. One retry waits at
	// a time, so retries can't take the places the user's turns need.
	retry := wait && before.request == m.RequestID
	// An opening is only ever the session's first turn, so one sent while another is taken is
	// refused at once, never cutting in on it as the user's own turn would.
	if m.Opening && wait && !retry {
		r.s.tell(r.conn, out{Type: "refused", RequestID: m.RequestID.String(), Reason: reasonOpeningRefused, Detail: openingRefused})
		return
	}
	full := retry && before.retrying != nil
	if wait && !full {
		select {
		case r.waiting <- struct{}{}:
		default:
			full = true
		}
	}
	if full {
		r.s.tell(r.conn, out{Type: "refused", RequestID: m.RequestID.String(), Reason: reasonBusy, Detail: "too many turns are waiting; send it again once the agent has answered"})
		return
	}
	speaking, stopSpeaking := context.WithCancel(ctx)
	run := &turnRun{request: m.RequestID, interrupted: make(chan struct{}), stopSpeaking: stopSpeaking, free: make(chan struct{})}
	if retry {
		run.retrying = before
	} else if before != nil {
		before.interrupt(received)
	}
	r.last = run
	r.turns.Go(func() {
		defer stopSpeaking()
		defer run.release()
		if wait {
			<-before.free // saved, cut where it was, before this turn's prompt
			<-r.waiting
		}
		if ctx.Err() != nil || r.caller.ExpiredAt(r.s.clock.Now()) {
			return // the connection ended, or is ending; the page sends it again on its next
		}
		r.s.runTurn(ctx, speaking, r.conn, r.caller, r.session, m, received, run, said, &r.aired)
	})
}

// interruptLast interrupts the turn begun last, if it is still being taken, at at.
func (r *replies) interruptLast(at time.Time) {
	r.mu.Lock()
	last := r.last
	r.mu.Unlock()
	if last != nil && !last.released() {
		last.interrupt(at)
	}
}

// begun returns the turn begun last if it is request's.
func (r *replies) begun(request uuid.UUID) *turnRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last == nil || r.last.request != request {
		return nil
	}
	return r.last
}

// beginSpoken begins a turn the user spoke before it returns, so whatever the
// microphone brings next comes after it.
func (r *replies) beginSpoken(ctx context.Context, h heardTurn) {
	r.begin(ctx, turnIn{Type: "turn", RequestID: h.request, Words: h.words}, r.s.clock.Now(), &h)
}

// turnRun is one turn being taken: its request, the turn it retries, if it
// does, what interrupting it closes and cancels, and when the next turn may
// start.
type turnRun struct {
	request       uuid.UUID
	retrying      *turnRun
	interrupted   chan struct{}
	stopSpeaking  context.CancelFunc
	interruptOnce sync.Once
	at            time.Time // when the user cut in; set before interrupted is closed
	free          chan struct{}
	freeOnce      sync.Once

	// mu orders the reply's first packet, its text reaching the page, the
	// user talking on and the turn being settled.
	// firstAt is when the first packet went, and sounding closes then.
	// talked says the user talked on before the reply reached them.
	// settled says the reply reached them, or stopped with none, before they
	// talked on: the turn is to be saved.
	// unremoved says the turn couldn't be taken back.
	mu                         sync.Mutex
	firstAt                    time.Time
	sounding                   chan struct{}
	talked, settled, unremoved bool
}

// stopForTalkOn stops the reply as the user talks on after their spoken turn was
// taken, having begun to speak at began, before they could hear any of it:
// unless its first packet had been heard by then, by the same estimate as the
// part of a reply heard, the turn has been settled or let the next start, or
// the user cut in otherwise. tell tells the page, before any more of the
// reply's text can reach it. It says whether it stopped the reply.
func (r *turnRun) stopForTalkOn(began time.Time, roundTrip time.Duration, tell func()) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	heardFirst := !r.firstAt.IsZero() && !heardBy(began.Add(-captureDelay), roundTrip).Before(r.firstAt)
	if _, cut := r.interruptedAt(); heardFirst || r.talked || r.settled || cut || r.released() {
		return false
	}
	r.talked = true
	tell()
	r.interrupt(began)
	return true
}

// talkedOn says whether the user talked on before the reply reached them.
func (r *turnRun) talkedOn() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.talked
}

// settle says the turn is to be saved, unless the user talked on first.
// After it says yes, talking on no longer stops the reply.
func (r *turnRun) settle() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settled = !r.talked
	return r.settled
}

// leftInFlight records that the turn couldn't be taken back, or took the
// place of one that couldn't.
func (r *turnRun) leftInFlight() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unremoved = true
}

// stillInFlight says whether the turn couldn't be taken back, or took the
// place of one that couldn't.
func (r *turnRun) stillInFlight() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.unremoved
}

// firstSound returns a channel closed once the reply's first packet goes out.
func (r *turnRun) firstSound() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sounding == nil {
		r.sounding = make(chan struct{})
	}
	return r.sounding
}

// sound says whether the reply's next packet, going out at at, may go: not
// once the user has talked on.
func (r *turnRun) sound(at time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.talked {
		return false
	}
	if r.firstAt.IsZero() {
		r.firstAt = at
		if r.sounding == nil {
			r.sounding = make(chan struct{})
		}
		close(r.sounding)
	}
	return true
}

// show tells the page a piece of the reply's text with tell, unless the user
// has cut in or talked on, and says whether it did.
func (r *turnRun) show(tell func()) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, cut := r.interruptedAt(); cut {
		return false
	}
	tell()
	return true
}

// gatedConn is a reply's connection: its packets go out only while the user
// hasn't talked on, and the first is noted.
type gatedConn struct {
	Connection
	run   *turnRun
	clock clock
}

var errTalkedOn = errors.New("the user talked on before the reply reached them")

func (c gatedConn) Play(opus []byte, silence time.Duration) error {
	if !c.run.sound(c.clock.Now()) {
		return errTalkedOn
	}
	return c.Connection.Play(opus, silence)
}

// interrupt stops the turn's reply and its speech at once, and the turn it
// retries, which may still be speaking; the first call counts.
func (r *turnRun) interrupt(at time.Time) {
	r.interruptOnce.Do(func() {
		r.at = at
		close(r.interrupted)
		r.stopSpeaking()
		if r.retrying != nil {
			r.retrying.interrupt(at)
		}
	})
}

// interruptedAt returns when the user cut in, if they have.
func (r *turnRun) interruptedAt() (time.Time, bool) {
	select {
	case <-r.interrupted:
		return r.at, true
	default:
		return time.Time{}, false
	}
}

// release lets the next turn start; the first call counts.
func (r *turnRun) release() { r.freeOnce.Do(func() { close(r.free) }) }

func (r *turnRun) released() bool {
	select {
	case <-r.free:
		return true
	default:
		return false
	}
}

// runTurn answers one turn: the reply's text goes to the browser as it arrives,
// and to text-to-speech, whose audio is played as it is made, until the
// reply ends or the user cuts in. It releases run for the next turn once the
// turn is saved, as far as it was heard, and its last message has gone.
// said is the turn as speech heard it, when the user spoke it; what is played
// is noted on aired. A spoken turn stays in flight until its reply reaches the
// user, or stops with none; if the user talks on before then, it is taken back
// and ends without another word to the page, and the words they go on to say
// are taken with its words as one turn, in its place.
func (s *Service) runTurn(ctx, speaking context.Context, conn Connection, c access.Caller, session uuid.UUID, m turnIn, received time.Time, run *turnRun, said *heardTurn, aired *aired) {
	request := m.RequestID.String()
	if said != nil && said.continues != nil && said.continues.stillInFlight() {
		// All the words can't take the place of a turn that couldn't be taken
		// back: it is still in flight, or, past the turn deadline, cut off
		// under the same request ID. They go back to the page, to send again.
		run.leftInFlight()
		s.tell(conn, out{Type: "refused", RequestID: request, Reason: reasonBusy, Detail: "the turn before couldn't be taken back; send it again"})
		return
	}
	req := conversation.TurnRequest{Session: session, RequestID: m.RequestID, Words: m.Words, Opening: m.Opening, Interrupt: run.interrupted, Hold: said != nil}
	for _, b := range m.ScreenInput {
		req.ScreenInput = append(req.ScreenInput, conversation.ScreenInputBlock(b))
	}
	text := make(chan string, textAhead)
	var audio played
	var playing sync.WaitGroup
	stopped := make(chan struct{})
	onAir := func(at time.Time, on bool) { aired.note(run, at, on) }
	playing.Go(func() {
		defer close(stopped)
		audio = play(speaking, gatedConn{Connection: conn, run: run, clock: s.clock}, s.tts.Speak(speaking, s.settings.Voice, text), s.clock, onAir)
	})
	var firstText time.Time
	turn, err := s.conversation.TakeTurn(ctx, c, req, s.forward(speaking, conn, run, request, text, &firstText))
	textDone := s.clock.Now()
	close(text)
	var failed *conversation.ReplyFailedError
	if req.Hold && (err == nil || errors.As(err, &failed)) { // in flight
		if !s.reached(speaking, conn, run, stopped, turn.SettleBy()) {
			playing.Wait()
			if err := s.conversation.TakeBack(ctx, turn); err != nil {
				s.log.Warn("take back a turn the user talked on after", "session", session, "err", err)
				run.leftInFlight()
			}
			return
		}
		if settled, settleErr := s.conversation.Settle(ctx, turn); settleErr != nil {
			turn, err = conversation.Turn{}, settleErr
		} else {
			turn = settled
		}
	}
	if run.talkedOn() { // before the turn was saved: nothing to take back
		playing.Wait()
		return
	}
	ending := s.ending(request, turn, err, !firstText.IsZero())
	s.tell(conn, ending)
	playing.Wait()
	if ending.Reason == reasonEnded {
		// The session ended, here or on another server: this connection learns
		// it from the turn refused, and ends, so the page stops listening.
		conn.Close(Ended)
		return
	}
	if ending.Type != "done" && ending.Type != "lost" {
		return // over at once: nothing was spoken
	}
	last, saved := out{Type: "spoken", RequestID: request, Failed: audio.err != nil}, turn
	// Cut in before the reply's text was complete, or before all of it was
	// heard: once it has all played, a new turn simply follows it.
	if at, ok := run.interruptedAt(); ok {
		heardTo := heard(audio.sent, at, conn.RoundTrip())
		if !at.After(textDone) || heardTo < len(strings.TrimRightFunc(turn.Reply, unicode.IsSpace)) {
			attrs := []any{"session", session, "turn", turn.ID}
			if !audio.stopped.IsZero() { // audio was playing
				attrs = append(attrs, "stopped_ms", max(audio.stopped.Sub(at), 0).Milliseconds())
			}
			if cut, err := s.conversation.Interrupted(ctx, turn, heardTo); err != nil {
				s.log.Warn("save where an interrupted reply was cut", "session", session, "err", err)
			} else if saved = cut; cut.Status == conversation.TurnCut {
				attrs = append(attrs, "saved_ms", s.clock.Now().Sub(at).Milliseconds())
			}
			said := saved.HeardReply() // what the agent remembers saying, so the page shows the same
			last = out{Type: "interrupted", RequestID: request, Heard: &said}
			s.log.Info("turn interrupted", attrs...)
		}
	}
	if said != nil && !audio.first.IsZero() {
		last.FirstAudioMS = audio.first.Sub(said.ended).Milliseconds()
	}
	s.tell(conn, last)
	run.release()
	// Timing and logging follow, still this turn's, while the next may start.
	if audio.err != nil {
		s.log.Warn("speak a reply", "session", session, "err", audio.err)
	}
	from, stages := stagesAt(received, firstText, audio, said)
	// Saved, not turn: the same turn's ID, workspace and user, and what a cut
	// changed, though the write reads only the stages. A turn whose save failed
	// is the zero turn: there is no row for stages to belong to.
	if saved.ID != uuid.Nil() {
		if err := s.conversation.RecordTiming(ctx, s.withSpeechStages(saved, from, stages)); err != nil {
			s.log.Warn("record a turn's stage times", "err", err)
		}
	}
	s.logStages(saved, from, stages, said)
}

// noticeLag is how long after speech begins it may be noticed: once the
// microphone packet holding the end of the frame it begins in has arrived, a
// frame and a packet later, the browser's packets being 20 ms as Chrome sends
// them.
const noticeLag = DetectionFrame*time.Second/ListenRate + 20*time.Millisecond

// reached waits until the turn's reply has begun to reach the user, or has
// stopped with none, and says whether it did before the user talked on. The
// reply reaches them once its first packet is heard, by the same estimate as
// the part of a reply heard; the turn waits a notice lag longer, so speech
// begun before then has been noticed. It waits no later than settleBy, the
// turn's own deadline for it.
func (s *Service) reached(speaking context.Context, conn Connection, run *turnRun, stopped <-chan struct{}, settleBy time.Time) bool {
	ctx, cancel := context.WithDeadline(speaking, settleBy)
	defer cancel()
	select {
	case <-run.firstSound():
	case <-stopped:
	case <-ctx.Done():
	}
	select {
	case <-run.firstSound():
		run.mu.Lock()
		first := run.firstAt
		run.mu.Unlock()
		s.clock.Until(ctx, first.Add(conn.RoundTrip()+browserDelay+captureDelay+noticeLag))
	default:
	}
	return run.settle()
}

// forward returns what TakeTurn calls with each piece of the reply: it goes
// to the page and to text-to-speech, unless the user has cut in or talked
// on, since a piece on its way as they did is neither shown nor said.
func (s *Service) forward(speaking context.Context, conn Connection, run *turnRun, request string, text chan<- string, firstText *time.Time) func(string) {
	return func(piece string) {
		if !run.show(func() { s.tell(conn, out{Type: "text", RequestID: request, Text: piece}) }) {
			return
		}
		if firstText.IsZero() {
			*firstText = s.clock.Now()
		}
		select {
		case text <- piece:
		case <-speaking.Done():
		}
	}
}
