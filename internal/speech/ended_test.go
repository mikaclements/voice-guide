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
	sessionsfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/speech"
	endofturnfake "github.com/mikaclements/voice-guide/internal/speech/endofturn/fake"
	ttsfake "github.com/mikaclements/voice-guide/internal/speech/tts/fake"
)

func TestATurnInASessionEndedElsewhereIsRefusedAndEndsTheConnection(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: packetsPerWord})
	c := h.connect(t)
	// Ended by a request to any server: this connection learns of it from its next turn.
	if _, err := h.conv.EndSession(context.Background(), h.caller, h.session); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	id := uuid.NewV7()
	c.Say(turnMessage(id, "Where should we start?"))

	if m := next(t, c); m.Type != "refused" || m.Reason != "ended" || m.RequestID != id.String() {
		t.Errorf("got %+v, want the turn refused as the session ended", m)
	}
	select {
	case <-c.Done():
	case <-time.After(wait):
		t.Fatalf("the connection is still open %s after its turn was refused as ended", wait)
	}
	if got := h.transport.Reasons(); !slices.Equal(got, []speech.CloseReason{speech.Ended}) {
		t.Errorf("closed with %v, want ended", got)
	}
	if asked, _ := h.model.(*modelfake.Model).Requests(); len(asked) != 0 {
		t.Errorf("the model was asked %d times, want never", len(asked))
	}
}

func TestAnEndedSessionGetsNoAudioConnection(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{})
	if _, err := h.conv.EndSession(context.Background(), h.caller, h.session); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	_, _, err := h.svc.Connect(context.Background(), h.caller, h.session, "offer")
	if !errors.Is(err, conversation.ErrSessionEnded) || h.transport.Answered() != 0 {
		t.Errorf("Connect = %v with %d offers answered; want ErrSessionEnded, none answered", err, h.transport.Answered())
	}
}

func TestASpokenTurnInAnEndedSessionIsRefusedAndEndsTheConnection(t *testing.T) {
	h := newHearingTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1},
		slog.New(slog.DiscardHandler), nil, speechThenQuiet, saying, endofturnfake.EndOfTurn{Answer: true})
	c := h.connect(t)
	if _, err := h.conv.EndSession(context.Background(), h.caller, h.session); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	speakFrames(c, 12)

	if m := until(t, c, "refused"); m[len(m)-1].Reason != "ended" {
		t.Errorf("got %+v, want the spoken turn refused as the session ended", m)
	}
	select {
	case <-c.Done():
	case <-time.After(wait):
		t.Fatalf("the connection is still open %s after its spoken turn was refused as ended", wait)
	}
	if got := h.transport.Reasons(); !slices.Equal(got, []speech.CloseReason{speech.Ended}) {
		t.Errorf("closed with %v, want ended", got)
	}
}

func TestHangingUpMidReplySavesItCutWhereTheUserStoppedHearingIt(t *testing.T) {
	h := newTurnHarness(t, &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: slowWords})
	id, _, err := h.svc.Connect(context.Background(), h.caller, h.session, "offer")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	c := h.transport.Conn(0)
	c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
	until(t, c, "done")
	// "Tell me" has played and " about Harbourline." has begun: its first word isn't heard yet.
	playedAtLeast(t, c, 2*slowWords+5)

	if err := h.svc.Hangup(context.Background(), h.caller, h.session, id); err != nil {
		t.Fatalf("Hangup: %v", err)
	}
	closeService(t, h.svc) // returns once the turn is saved
	saved := h.turns.All()
	if len(saved) != 1 || saved[0].Status != conversation.TurnCut || saved[0].HeardReply() != "Tell me" {
		t.Errorf("saved %+v, want the reply cut after %q, where the user stopped hearing it", saved, "Tell me")
	}
}

// touching is the fake store of sessions, telling the test each session it marks active.
type touching struct {
	*sessionsfake.Sessions
	touched chan uuid.UUID
}

func (s touching) Touch(ctx context.Context, workspace, user, id uuid.UUID) (bool, error) {
	live, err := s.Sessions.Touch(ctx, workspace, user, id)
	select {
	case s.touched <- id:
	default: // the test reads only the first
	}
	return live, err
}

func TestSpeechHeardMarksTheSessionActive(t *testing.T) {
	sessions := touching{Sessions: sessionsfake.New(), touched: make(chan uuid.UUID, 1)}
	h := newStoredTurnHarness(t, sessions, sessionsfake.NewTurnsFor(sessions.Sessions), &modelfake.Model{Pieces: replyPieces}, ttsfake.TextToSpeech{PacketsPerWord: 1},
		slog.New(slog.DiscardHandler), speechThenQuiet, saying, endofturnfake.EndOfTurn{Answer: true})
	clock := time.Date(2026, 11, 18, 9, 4, 50, 0, time.UTC)
	sessions.SetClock(func() time.Time { return clock })
	c := h.connect(t)
	speakFrames(c, 12)
	select {
	case id := <-sessions.touched:
		if id != h.session || !sessions.LastActive(h.session).Equal(clock) {
			t.Errorf("marked %s active at %s, want the session %s at %s", id, sessions.LastActive(h.session), h.session, clock)
		}
	case <-time.After(wait):
		t.Fatalf("the session wasn't marked active within %s of the user speaking", wait)
	}
}
