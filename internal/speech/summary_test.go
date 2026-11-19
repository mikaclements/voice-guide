package speech_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	agentsfake "github.com/mikaclements/voice-guide/internal/agents/store/fake"
	"github.com/mikaclements/voice-guide/internal/conversation"
	modelfake "github.com/mikaclements/voice-guide/internal/conversation/model/fake"
	sessionsfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/speech"
	detectionfake "github.com/mikaclements/voice-guide/internal/speech/detection/fake"
	"github.com/mikaclements/voice-guide/internal/speech/transport/fake"
	ttsfake "github.com/mikaclements/voice-guide/internal/speech/tts/fake"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	itemsfake "github.com/mikaclements/voice-guide/internal/usercontext/store/fake"
)

func TestEndingAfterThePageHangsUpSummarizesTheReplyOnlyAsFarAsItWasHeard(t *testing.T) {
	versions := agentsfake.New()
	if _, err := agents.Publish(context.Background(), versions, workspace, []agents.Agent{{
		ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea.", Summary: "Note what was covered.",
	}}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	sessions := sessionsfake.New()
	turns := sessionsfake.NewTurnsFor(sessions)
	model := &modelfake.Model{Pieces: replyPieces}
	conv := conversation.NewService(agents.NewCatalog(versions), sessions, turns, model, usercontext.NewService(itemsfake.New()),
		conversation.Settings{UserContextChars: 1000, SessionContextChars: 1000, ScreenInputChars: 1000, WordsChars: 1000, TurnDeadline: time.Minute, Model: "claude-test"})
	s, _, err := conv.StartSession(context.Background(), product, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), User: "aiko-42"})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	// The session's user, as their browser calls.
	caller := access.Caller{Workspace: workspace, User: s.User, Expires: expires}
	transport := fake.New()
	svc := speech.NewService(conv, transport, ttsfake.TextToSpeech{PacketsPerWord: slowWords}, detectionfake.Detection{}, nil, nil,
		speech.Settings{MaxConnections: 10, Voice: "af_heart", TextToSpeech: "fake", Listening: startingListening}, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { closeService(t, svc) })
	id, _, err := svc.Connect(context.Background(), caller, s.ID, "offer")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	c := transport.Conn(0)
	c.Say(turnMessage(uuid.NewV7(), "Where should we start?"))
	until(t, c, "done")
	playedAtLeast(t, c, 2*slowWords+5) // "Tell me" has played, " about Harbourline." hasn't

	// The page's End: the audio first, then the session.
	if err := svc.Hangup(context.Background(), caller, s.ID, id); err != nil {
		t.Fatalf("Hangup: %v", err)
	}
	if _, err := conv.EndSession(context.Background(), caller, s.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if err := conv.Summarize(context.Background(), workspace, s.User, conversation.SummaryArgs{Session: s.ID}); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	asked, _ := model.Requests()
	if words := asked[len(asked)-1].NewWords; !strings.Contains(words, "Agent: Tell me—") || strings.Contains(words, "Harbourline") {
		t.Errorf("the summary was asked with %q, want the reply only as far as it was heard", words)
	}
}
