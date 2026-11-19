package conversation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/usercontext"
)

// Turn is one exchange of a session: the screen input and words the user
// sent, and the agent's reply exactly as saved.
type Turn struct {
	ID, Workspace, User, Session uuid.UUID
	// RequestID is the caller's, so a turn sent twice can be recognised.
	RequestID uuid.UUID
	// Seq numbers the session's turns from 1.
	Seq         int
	ScreenInput []ScreenInputBlock
	Words       string
	// Reply is the whole reply, as the model sent it, even when the user heard
	// only part of it; HeardReply is the part they heard.
	Reply  string
	Status TurnStatus
	// Heard is how many bytes of Reply the user heard, for a cut turn.
	Heard int
	// Unfinished says the user's interrupt came before the model finished its
	// reply, so even a reply heard to its last word was cut short. Set on the
	// turn TakeTurn returns; not stored.
	Unfinished bool
	// Opening says the agent opened the session with this turn: the first,
	// with no words, the user's message in its prompt a fixed cue.
	Opening bool
	// Stages are the times TakeTurn measured, for RecordTiming to save.
	Stages []Stage
	// settleBy is when a turn held in flight must be settled, if it is.
	settleBy time.Time
}

// SettleBy is when a turn TakeTurn held in flight must be settled or taken
// back: its reply's deadline, so the turn still ends within the turn deadline,
// after which the session's next turn may start without it.
func (t Turn) SettleBy() time.Time { return t.settleBy }

// HeardReply is the part of the reply the user heard: all of it, unless the
// turn was cut.
func (t Turn) HeardReply() string {
	if t.Status == TurnCut {
		return t.Reply[:t.Heard]
	}
	return t.Reply
}

// TurnStatus says how far a turn got: in flight, answered, lost when the
// model gave no full reply or the turn outlived the server answering it, or
// cut when the user interrupted the reply and heard only part of it.
type TurnStatus string

const (
	TurnInFlight TurnStatus = "in_flight"
	TurnAnswered TurnStatus = "answered"
	TurnLost     TurnStatus = "lost"
	TurnCut      TurnStatus = "cut"
)

// Turns keeps the turns of sessions.
type Turns interface {
	// Start saves t in flight as its session's next turn and returns its
	// number. after is the number of the last finished turn the prompt was
	// built on, 0 for none. It returns ErrTurnInFlight while the session has
	// another turn in flight that started less than deadline ago, or once a
	// turn after `after` has finished, so no turn is answered without the one
	// before it; a turn in flight for longer was cut off, and is saved lost.
	// It returns ErrOpeningRefused for an opening once the session has any
	// turn, so an opening is only ever turn 1.
	// It returns ErrTurnRepeated for a request ID the session already used,
	// and ErrSessionEnded once the session has ended: an end and a start at
	// once are taken in some order, so a turn that starts is in flight when
	// the end lands, never after it, and a retry of a turn the session took
	// before it ended is answered as a repeat would be. A turn starting marks
	// its session active.
	Start(ctx context.Context, t Turn, after int, deadline time.Duration) (int, error)
	// Finish saves how the turn in flight ended: its status and reply. It
	// refuses a turn a later turn of the session has followed.
	Finish(ctx context.Context, t Turn) error
	// Cut saves that the user heard only the first t.Heard bytes of the
	// answered turn t's reply. It refuses a turn not answered, one whose saved
	// reply doesn't start with those bytes, and one a later turn of the
	// session has followed.
	Cut(ctx context.Context, t Turn) error
	// Remove deletes the turn t, as if never taken. It refuses a turn no
	// longer in flight.
	Remove(ctx context.Context, t Turn) error
	// Finished returns the session's turns no longer in flight, oldest first.
	Finished(ctx context.Context, workspace, session uuid.UUID) ([]Turn, error)
	// ForRequest returns the status of the session's turn with requestID, if
	// it has one.
	ForRequest(ctx context.Context, workspace, session, requestID uuid.UUID) (TurnStatus, bool, error)
	// InFlight says whether the session has a turn in flight that started
	// less than deadline ago: one older was cut off, by a crash say.
	InFlight(ctx context.Context, workspace, session uuid.UUID, deadline time.Duration) (bool, error)
	// CutOff saves lost every turn of the session still in flight that
	// started at least deadline ago, as the session's next turn would.
	CutOff(ctx context.Context, workspace, session uuid.UUID, deadline time.Duration) error
	// RecordStages saves the stage times of a turn already saved.
	RecordStages(ctx context.Context, t Turn) error
}

// TurnRequest is a turn a user takes: the caller's request ID, the words, and
// the screen input the product sent with them.
type TurnRequest struct {
	Session     uuid.UUID
	RequestID   uuid.UUID
	Words       string
	ScreenInput []ScreenInputBlock
	// Interrupt, when closed, stops the reply: the user cut in. The turn is
	// saved answered with the text sent so far, unless the model had already
	// failed, and the words are saved whenever it closes. A piece being handed
	// to send as it closes may still arrive, so a caller that closes it drops
	// any text after. Nil for a caller that never interrupts.
	Interrupt <-chan struct{}
	// Hold keeps the turn in flight once its reply ends, for the caller to
	// Settle once the reply has begun to reach the user, or TakeBack if the
	// user talked on first: a spoken turn, whose words the user may go on
	// with until they hear the agent.
	Hold bool
	// Opening asks the agent to open the session: no words, taken only as
	// the first turn of a session whose agent version opens.
	Opening bool
}

// ErrNoSuchSession reports a session the caller can't use: missing, or
// another workspace's or user's, all alike, so a caller learns nothing about
// sessions outside its reach.
var ErrNoSuchSession = errors.New("no such session")

// ErrTurnInFlight reports a turn sent while the session answers another, or
// after another finished that its prompt couldn't hold.
var ErrTurnInFlight = errors.New("the session has a turn in flight")

// ErrTurnRepeated reports a request ID the session already took a turn with.
var ErrTurnRepeated = errors.New("the request ID already took a turn in the session")

// ErrOpeningRefused reports an opening the session can't take: it already
// has a turn, or its agent version waits for the user. The agent doesn't
// open, and the caller has nothing to do.
var ErrOpeningRefused = errors.New("the agent doesn't open this session")

// ErrWordsUnusable reports words a turn can't take, saying why.
var ErrWordsUnusable = errors.New("the words can't be used")

// ErrWordsTooLong reports words over the character limit.
var ErrWordsTooLong = errors.New("the words are over the limit")

// saveTime is kept back from the end of a turn's deadline for saving how the
// turn ended, so a reply that runs to the deadline is still saved within it;
// half of a deadline shorter than twice this. A save is one row update, which
// takes milliseconds, so 5s is a wide margin: a round guess.
const saveTime = 5 * time.Second

// TakeTurn answers the user's words in one of their sessions, calling send
// with each piece of the reply as it arrives, and returns the turn as saved.
// The words are saved alongside the model call, the reply exactly as sent, so
// the next turn's prompt holds the model's own words; a reply the model
// didn't finish is saved as lost, with the text sent so far, and returned
// with a *ReplyFailedError. The turn ends within the turn deadline, after
// which the session's next turn may start without it. A turn the request
// holds is returned still in flight, how it ended not yet saved.
func (s *Service) TakeTurn(ctx context.Context, c access.Caller, r TurnRequest, send func(text string)) (Turn, error) {
	keep := min(saveTime, s.settings.TurnDeadline/2)
	ctx, cancel := context.WithTimeout(ctx, s.settings.TurnDeadline-keep)
	defer cancel()
	if err := checkWords(r, s.settings.WordsChars); err != nil {
		return Turn{}, err
	}
	if err := checkScreenInput(r.ScreenInput, s.settings.ScreenInputChars); err != nil {
		return Turn{}, err
	}
	session, err := s.Session(ctx, c, r.Session)
	if err != nil {
		return Turn{}, err
	}
	// Refused before the model is asked; one ending after this is refused when
	// the turn starts.
	if session.Ended() {
		return Turn{}, s.endedFor(ctx, session, r.RequestID)
	}
	promptStage := Stage{Started: s.now()}
	prompt, after, model, err := s.prompt(ctx, session, r)
	if err != nil {
		return Turn{}, err
	}
	promptStage.Took = s.now().Sub(promptStage.Started)

	turn := Turn{
		ID: uuid.NewV7(), Workspace: session.Workspace, User: session.User, Session: session.ID,
		RequestID: r.RequestID, ScreenInput: r.ScreenInput, Words: r.Words, Opening: r.Opening,
	}
	if closed(r.Interrupt) {
		return s.wordsOnly(ctx, turn, after, keep, promptStage, r.Hold)
	}
	// The model starts before the words are saved, so saving costs the user
	// no time; its text waits until they are, and a refused save stops it.
	replyCtx, stop := context.WithCancel(ctx)
	replyStage := Stage{Started: s.now()}
	events := s.model.Reply(replyCtx, ModelRequest{Model: model, Session: session.ID, Prompt: prompt})
	defer func() {
		stop()
		for range events { // the reply is over only once the model lets go of it
		}
	}()
	if turn.Seq, err = s.turns.Start(ctx, turn, after, s.settings.TurnDeadline); err != nil {
		return Turn{}, fmt.Errorf("start the turn: %w", err)
	}

	var reply strings.Builder
	var firstText time.Time
	var report *ModelReport
	failed := error(&ReplyFailedError{Kind: "unavailable"}) // until a final event says otherwise
	ended := false                                          // the model sent its final event
	final := func(e ModelEvent) {                           // the model's last word: finished, or failed
		switch {
		case e.Done:
			failed, report, ended = nil, e.Report, true
		case e.Error != "":
			failed, ended = &ReplyFailedError{Kind: e.Error}, true
		}
	}
	for {
		var e ModelEvent
		ok, got := true, false
		select {
		case e, ok = <-events:
			got = true
		case <-r.Interrupt:
		}
		// The interrupt is checked again before any text goes out: with both
		// ready, select picks either. A final event still counts, so a reply
		// that failed as the user cut in stays failed.
		interrupted := closed(r.Interrupt)
		switch {
		case !got || !ok:
		case e.Done || e.Error != "":
			final(e)
		case !interrupted:
			if firstText.IsZero() {
				firstText = s.now()
			}
			reply.WriteString(e.Text)
			send(e.Text)
		}
		if interrupted {
			if !got { // the interrupt won the select: a final event already waiting still counts
				select {
				case e, ok := <-events:
					if ok {
						final(e)
					}
				default:
				}
			}
			stop() // now, not after the save: the model would go on writing meanwhile
			if !ended {
				failed, turn.Unfinished = nil, true // the reply ends where the user cut in, not by failing
			}
			break
		}
		if !ok {
			break
		}
	}
	replyStage.Took = s.now().Sub(replyStage.Started)
	turn.Reply, turn.Status = reply.String(), TurnAnswered
	if failed != nil {
		turn.Status = TurnLost
	}
	if r.Hold {
		turn.Stages = stagesOf(promptStage, replyStage, Stage{}, firstText, report, model)
		turn.settleBy, _ = ctx.Deadline()
		return turn, failed
	}
	// Saved even once the caller has gone, or the turn would hold up its
	// session until the deadline, its words unsaved.
	saveCtx, cancelSave := saveContext(ctx, keep)
	defer cancelSave()
	saveStage := Stage{Started: s.now()}
	if err := s.turns.Finish(saveCtx, turn); err != nil {
		return Turn{}, saveFailed(turn, failed, err)
	}
	saveStage.Took = s.now().Sub(saveStage.Started)
	turn.Stages = stagesOf(promptStage, replyStage, saveStage, firstText, report, model)
	return turn, failed
}

// endedFor says why a turn with requestID is refused in the ended session.
// A retry of a turn taken just before the end is answered as a repeat
// would be: ErrTurnInFlight while that turn is still in flight, as the
// caller hears for any repeat of a turn in flight, and ErrTurnRepeated once
// it has finished, so the caller counts it as taken. Otherwise it is
// ErrSessionEnded.
func (s *Service) endedFor(ctx context.Context, session Session, requestID uuid.UUID) error {
	status, found, err := s.turns.ForRequest(ctx, session.Workspace, session.ID, requestID)
	switch {
	case err != nil:
		return fmt.Errorf("look up the request's turn: %w", err)
	case !found:
		return ErrSessionEnded
	case status == TurnInFlight:
		return ErrTurnInFlight
	}
	return ErrTurnRepeated
}

// saveFailed reports that how turn t ended couldn't be saved, naming the
// turn, which stays in flight and holds up its session until the deadline,
// and why its reply ended when it failed. It isn't the reply's failure, which
// callers answer by naming a turn saved as ended.
func saveFailed(t Turn, failed, err error) error {
	why := ""
	if failed != nil {
		why = " (" + failed.Error() + ")"
	}
	return fmt.Errorf("save how turn %s, number %d, ended%s: %w", t.ID, t.Seq, why, err)
}

// saveContext bounds saving how a turn ended, which runs even once the caller
// has gone: the time kept for it, and never past the turn's deadline, which is
// that time after the deadline of the turn's ctx. Counting from that deadline,
// not from the reply's end, matters for a model the deadline stops, which ends
// a little after it.
func saveContext(ctx context.Context, keep time.Duration) (context.Context, context.CancelFunc) {
	replyBy, _ := ctx.Deadline()
	byTurn, cancelTurn := context.WithDeadline(context.WithoutCancel(ctx), replyBy.Add(keep))
	saveCtx, cancelSave := context.WithTimeout(byTurn, keep)
	return saveCtx, func() {
		cancelSave()
		cancelTurn()
	}
}

// wordsOnly saves the words of a turn the user cut in on before the model was
// asked: no one would hear the reply, so the model isn't asked for one, and
// the turn records only its prompt and save stages, the save timing both
// writes, since no model runs alongside the first; one held stays in flight.
func (s *Service) wordsOnly(ctx context.Context, turn Turn, after int, keep time.Duration, promptStage Stage, hold bool) (Turn, error) {
	var err error
	saveStage := Stage{Name: StageSave, Started: s.now()}
	if turn.Seq, err = s.turns.Start(ctx, turn, after, s.settings.TurnDeadline); err != nil {
		return Turn{}, fmt.Errorf("start the turn: %w", err)
	}
	turn.Status, turn.Unfinished = TurnAnswered, true
	if hold {
		promptStage.Name = StagePrompt
		turn.Stages = []Stage{promptStage, {Name: StageSave}}
		turn.settleBy, _ = ctx.Deadline()
		return turn, nil
	}
	saveCtx, cancelSave := saveContext(ctx, keep)
	defer cancelSave()
	if err := s.turns.Finish(saveCtx, turn); err != nil {
		return Turn{}, saveFailed(turn, nil, err)
	}
	saveStage.Took = s.now().Sub(saveStage.Started)
	promptStage.Name = StagePrompt
	turn.Stages = []Stage{promptStage, saveStage}
	return turn, nil
}

// closed reports whether ch is closed; a nil channel never is.
func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Settle saves how turn t, held in flight by its request, ended, as TakeTurn
// returned it, and returns it with its save timed. The write runs even once
// the caller has gone, within the time a turn keeps for saving, and never past
// the turn's deadline, which is that time after it was to be settled by.
func (s *Service) Settle(ctx context.Context, t Turn) (Turn, error) {
	ctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), t.settleBy)
	defer cancel()
	ctx, cancelSave := saveContext(ctx, min(saveTime, s.settings.TurnDeadline/2))
	defer cancelSave()
	save := Stage{Name: StageSave, Started: s.now()}
	if err := s.turns.Finish(ctx, t); err != nil {
		return Turn{}, saveFailed(t, nil, err)
	}
	save.Took = s.now().Sub(save.Started)
	t.Stages = append(slices.Clone(t.Stages[:len(t.Stages)-1]), save)
	return t, nil
}

// TakeBack takes back turn t, held in flight by its request, when the user
// talked on before any of its reply reached them: it is removed, as if never
// taken, so their words, all of them, are taken as one turn in its place, and
// no prompt holds a reply nobody heard. Nothing else can have started in the
// session meanwhile, one turn being in flight at a time. The write runs even
// once the caller has gone, within the same short deadline as saving a cut.
func (s *Service) TakeBack(ctx context.Context, t Turn) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cutWrite)
	defer cancel()
	if err := s.turns.Remove(ctx, t); err != nil {
		return fmt.Errorf("take the turn back: %w", err)
	}
	return nil
}

// cutWrite bounds saving where a reply was cut, which the session's next turn
// waits for. It is one row update, milliseconds on a healthy database, so 2s
// only stops a stalled one from holding the next turn: a round guess.
const cutWrite = 2 * time.Second

// Interrupted saves that the user heard only the first heard bytes of turn
// t's reply, as TakeTurn returned it, and returns the turn as saved. The whole
// reply is kept; later prompts hold only the part heard. A point inside a
// character moves back to its start. A reply heard to its last word, trailing
// space aside, is left as it was, unless the interrupt came before the model
// finished it, when it is cut with all of it heard. A reply the model
// failed to finish is left as it was, lost. A turn interrupted before any
// reply is cut with nothing heard, so the model learns the user cut in
// rather than that it said nothing.
// Call it before the session's next turn, whose prompt must not show the
// model words the user never heard. The write runs even once the caller has
// gone, within its own short deadline.
func (s *Service) Interrupted(ctx context.Context, t Turn, heard int) (Turn, error) {
	spoken := len(strings.TrimRightFunc(t.Reply, unicode.IsSpace))
	if t.Status != TurnAnswered {
		return t, nil
	}
	if spoken > 0 && heard >= spoken {
		if !t.Unfinished {
			return t, nil
		}
		heard = len(t.Reply) // all of it heard, but the model was cut short
	}
	heard = max(heard, 0)
	for heard > 0 && heard < len(t.Reply) && !utf8.RuneStart(t.Reply[heard]) {
		heard--
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cutWrite)
	defer cancel()
	t.Status, t.Heard = TurnCut, heard
	if err := s.turns.Cut(ctx, t); err != nil {
		return Turn{}, fmt.Errorf("save where the reply was cut: %w", err)
	}
	return t, nil
}

// prompt builds the turn's prompt from what the session recorded and the
// turns saved before it, and returns it with the number of the last of them
// and the model that answers it.
func (s *Service) prompt(ctx context.Context, session Session, r TurnRequest) (Prompt, int, string, error) {
	version, err := s.catalog.Version(ctx, session.Workspace, session.Agent, session.AgentVersion)
	if err != nil {
		return Prompt{}, 0, "", fmt.Errorf("read the session's agent version: %w", err)
	}
	var items []usercontext.Item
	if len(session.ContextItems) > 0 { // most sessions load nothing, and needn't ask
		items, err = s.userContext.Items(ctx, usercontext.ItemsRequest{Workspace: session.Workspace, User: session.User, IDs: session.ContextItems})
		if err != nil {
			return Prompt{}, 0, "", fmt.Errorf("read what the session loaded: %w", err)
		}
	}
	saved, err := s.turns.Finished(ctx, session.Workspace, session.ID)
	if err != nil {
		return Prompt{}, 0, "", fmt.Errorf("read the session's turns: %w", err)
	}
	// A repeat of a finished turn is refused before the model is asked, so it costs no model call
	// and leaves the session's model process for the next real turn. The unique key on the
	// request ID still refuses a repeat that races the first.
	for _, t := range saved {
		if t.RequestID == r.RequestID {
			return Prompt{}, 0, "", ErrTurnRepeated
		}
	}
	// Refused here too, before the model is asked; the turn's start refuses one racing another turn.
	if r.Opening && (!version.Opens || len(saved) > 0) {
		return Prompt{}, 0, "", ErrOpeningRefused
	}
	last := 0
	if len(saved) > 0 {
		last = saved[len(saved)-1].Seq
	}
	return buildPrompt(version.Persona, session.SessionContext, items, saved, Turn{ScreenInput: r.ScreenInput, Words: r.Words, Opening: r.Opening}), last, s.modelOf(version), nil
}

// checkWords refuses words a turn can't take: none, unless screen input
// says what the turn is about; a NUL character, which can't be stored; or
// over limit characters. An opening takes none: the agent speaks first.
func checkWords(r TurnRequest, limit int) error {
	switch n := utf8.RuneCountInString(r.Words); {
	case r.Opening && n > 0:
		return fmt.Errorf("%w: an opening carries no words, since the agent speaks first", ErrWordsUnusable)
	case r.Opening:
		return nil
	case n == 0 && len(r.ScreenInput) == 0:
		return fmt.Errorf("%w: a turn needs words, screen input or both", ErrWordsUnusable)
	case strings.ContainsRune(r.Words, 0):
		return fmt.Errorf("%w: they hold a NUL character, which can't be stored", ErrWordsUnusable)
	case n > limit:
		return fmt.Errorf("%w: %d characters, at most %d", ErrWordsTooLong, n, limit)
	}
	return nil
}
