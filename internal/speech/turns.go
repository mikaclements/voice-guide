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

// runTurn answers one turn: the reply's text goes to the browser as it arrives,
// and to text-to-speech, whose audio is played as it is made, until the
// reply ends or the user cuts in. It releases run for the next turn once the
// turn is saved, as far as it was heard, and its last message has gone.
// said is the turn as speech heard it, when the user spoke it; what is played
// is noted in r.aired. A spoken turn stays in flight until its reply reaches the
// user, or stops with none; if the user talks on before then, it is taken back
// and ends without another word to the page, and the words they go on to say
// are taken with its words as one turn, in its place.
func (r *replies) runTurn(ctx, speaking context.Context, m turnIn, received time.Time, run *turnRun, said *heardTurn) {
	s := r.s
	request := m.RequestID.String()
	if said != nil && said.continues != nil && said.continues.stillInFlight() {
		// All the words can't take the place of a turn that couldn't be taken
		// back: it is still in flight, or, past the turn deadline, cut off
		// under the same request ID. They go back to the page, to send again.
		run.leftInFlight()
		s.tell(r.conn, out{Type: "refused", RequestID: request, Reason: reasonBusy, Detail: "the turn before couldn't be taken back; send it again"})
		return
	}
	req := conversation.TurnRequest{Session: r.session, RequestID: m.RequestID, Words: m.Words, Opening: m.Opening, Interrupt: run.interrupted, Hold: said != nil}
	for _, b := range m.ScreenInput {
		req.ScreenInput = append(req.ScreenInput, conversation.ScreenInputBlock(b))
	}
	text := make(chan string, textAhead)
	var audio played
	var playing sync.WaitGroup
	stopped := make(chan struct{})
	onAir := func(at time.Time, on bool) { r.aired.note(run, at, on) }
	playing.Go(func() {
		defer close(stopped)
		audio = play(speaking, gatedConn{Connection: r.conn, run: run, clock: s.clock}, s.tts.Speak(speaking, s.settings.Voice, text), s.clock, onAir)
	})
	var firstText time.Time
	turn, err := s.conversation.TakeTurn(ctx, r.caller, req, s.forward(speaking, r.conn, run, request, text, &firstText))
	textDone := s.clock.Now()
	close(text)
	var failed *conversation.ReplyFailedError
	if req.Hold && (err == nil || errors.As(err, &failed)) { // in flight
		if !s.reached(speaking, r.conn, run, stopped, turn.SettleBy()) {
			playing.Wait()
			if err := s.conversation.TakeBack(ctx, turn); err != nil {
				s.log.Warn("take back a turn the user talked on after", "session", r.session, "err", err)
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
	s.tell(r.conn, ending)
	playing.Wait()
	if ending.Reason == reasonEnded {
		// The session ended, here or on another server: this connection learns
		// it from the turn refused, and ends, so the page stops listening.
		r.conn.Close(Ended)
		return
	}
	if ending.Type != "done" && ending.Type != "lost" {
		return // over at once: nothing was spoken
	}
	last, saved := out{Type: "spoken", RequestID: request, Failed: audio.err != nil}, turn
	// Cut in before the reply's text was complete, or before all of it was
	// heard: once it has all played, a new turn simply follows it.
	if at, ok := run.interruptedAt(); ok {
		heardTo := heard(audio.sent, at, r.conn.RoundTrip())
		if !at.After(textDone) || heardTo < len(strings.TrimRightFunc(turn.Reply, unicode.IsSpace)) {
			attrs := []any{"session", r.session, "turn", turn.ID}
			if !audio.stopped.IsZero() { // audio was playing
				attrs = append(attrs, "stopped_ms", max(audio.stopped.Sub(at), 0).Milliseconds())
			}
			if cut, err := s.conversation.Interrupted(ctx, turn, heardTo); err != nil {
				s.log.Warn("save where an interrupted reply was cut", "session", r.session, "err", err)
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
	s.tell(r.conn, last)
	run.release()
	// Timing and logging follow, still this turn's, while the next may start.
	if audio.err != nil {
		s.log.Warn("speak a reply", "session", r.session, "err", audio.err)
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
