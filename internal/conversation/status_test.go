package conversation_test

import (
	"context"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/usercontext"
)

// read returns the session as saved now.
func (h serviceOverFakes) read(t *testing.T, id uuid.UUID) conversation.Session {
	t.Helper()
	s, found, err := h.sessions.ByID(context.Background(), workspace, user, id)
	if err != nil || !found {
		t.Fatalf("ByID = %v, %v", found, err)
	}
	return s
}

func TestASummarysStatusIsPendingFromTheEndThenWrittenOrNoneAndNeverBack(t *testing.T) {
	t.Run("live, then pending, then written", func(t *testing.T) {
		h := newServiceOverFakes(t)
		h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea.", Summary: instruction})
		s, _ := h.start(t, caller, keeperID, uuid.NewV7())
		h.takeTurn(t, turnIn(s.ID, "Is the lamp lit?"))
		if got := h.read(t, s.ID).SummaryStatus; got != "" {
			t.Errorf("a live session's status is %q, want none", got)
		}
		_, _ = h.svc.EndSession(context.Background(), caller, s.ID)
		if got := h.read(t, s.ID).SummaryStatus; got != conversation.SummaryPending {
			t.Errorf("an ended session's status is %q, want pending", got)
		}
		_ = h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID})
		_ = h.svc.SettleSummary(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID})
		if got := h.read(t, s.ID).SummaryStatus; got != conversation.SummaryWritten {
			t.Errorf("status %q once written and settled, want written", got)
		}
	})
	t.Run("none when the agent asks for none or the user said nothing", func(t *testing.T) {
		h := newServiceOverFakes(t)
		silent, _ := h.start(t, caller, captainID, uuid.NewV7())
		noSummary, _ := h.start(t, caller, keeperID, uuid.NewV7())
		h.takeTurn(t, turnIn(noSummary.ID, "Is the lamp lit?"))
		for _, s := range []conversation.Session{silent, noSummary} {
			_, _ = h.svc.EndSession(context.Background(), caller, s.ID)
			if err := h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID}); err != nil {
				t.Fatalf("Summarize: %v", err)
			}
			if got := h.read(t, s.ID).SummaryStatus; got != conversation.SummaryNone {
				t.Errorf("status %q, want none", got)
			}
		}
	})
	t.Run("none once the summary job failed for good", func(t *testing.T) {
		h, s := summarizing(t)
		h.model.Fail, h.model.Pieces = "overloaded", nil
		if err := h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID}); err == nil {
			t.Fatal("Summarize = nil with the model failing")
		}
		if err := h.svc.SettleSummary(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID}); err != nil {
			t.Fatalf("SettleSummary: %v", err)
		}
		if got := h.read(t, s.ID).SummaryStatus; got != conversation.SummaryNone {
			t.Errorf("status %q once the job failed for good, want none", got)
		}
	})
}

func TestASummarySavedBeforeItsJobFailedForGoodSettlesAsWritten(t *testing.T) {
	// The text saved, then a rerun failed for good: the status settles as written, and never moves back.
	h, s := summarizing(t)
	ctx := context.Background()
	if _, err := usercontext.NewService(h.items).SaveSummary(ctx, usercontext.SummaryRequest{Workspace: workspace, User: user, Agent: keeperID, Session: s.ID, SessionEnded: h.read(t, s.ID).EndedAt, Content: "Covered the lamp."}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	if err := h.svc.SettleSummary(ctx, workspace, user, conversation.SummaryArgs{Session: s.ID}); err != nil {
		t.Fatalf("SettleSummary: %v", err)
	}
	_ = h.sessions.SettleSummary(ctx, workspace, user, s.ID, conversation.SummaryNone)
	if got := h.read(t, s.ID).SummaryStatus; got != conversation.SummaryWritten {
		t.Errorf("status %q, want written, kept once settled", got)
	}
}
