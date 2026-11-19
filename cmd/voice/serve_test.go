package main

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	agentsfake "github.com/mikaclements/voice-guide/internal/agents/store/fake"
	"github.com/mikaclements/voice-guide/internal/api"
	"github.com/mikaclements/voice-guide/internal/conversation"
	conversationhttp "github.com/mikaclements/voice-guide/internal/conversation/http"
	conversationfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/ticket"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	itemsfake "github.com/mikaclements/voice-guide/internal/usercontext/store/fake"
)

// stalledModel sends its text, then nothing more until the reply is stopped.
type stalledModel struct{ text string }

func (m stalledModel) Reply(ctx context.Context, _ conversation.ModelRequest) <-chan conversation.ModelEvent {
	events := make(chan conversation.ModelEvent, 1)
	events <- conversation.ModelEvent{Text: m.text}
	go func() {
		<-ctx.Done()
		close(events)
	}()
	return events
}

// A turn may stream for longer than a stopping service is given; it ends when
// voice stops, saved and its caller told, instead of being left in flight.
func TestATurnInFlightWhenVoiceStopsIsEndedAndSaved(t *testing.T) {
	ctx := context.Background()
	workspace, agent := uuid.NewV7(), uuid.NewV7()
	versions := agentsfake.New()
	if _, err := agents.Publish(ctx, versions, workspace, []agents.Agent{{ID: agent, Name: "Lighthouse keeper", Persona: "You keep a lighthouse."}}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	turns := conversationfake.NewTurns()
	svc := conversation.NewService(agents.NewCatalog(versions), conversationfake.New(), turns, stalledModel{"Bearing"}, usercontext.NewService(itemsfake.New()),
		conversation.Settings{UserContextChars: 100, SessionContextChars: 100, ScreenInputChars: 100, WordsChars: 100, TurnDeadline: time.Hour, Model: "claude-test"})
	session, _, err := svc.StartSession(ctx, access.Caller{Workspace: workspace, Product: true}, conversation.StartRequest{Agent: agent, RequestID: uuid.NewV7(), User: "aiko-42"})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	key, _ := ticket.NewKey([]byte("0123456789abcdef0123456789abcdef"))
	ticket, err := key.MintTicket(access.Caller{Workspace: workspace, User: session.User, Session: session.ID}, session.StartedAt, time.Now())
	if err != nil {
		t.Fatalf("MintTicket: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	rt := api.NewRouter(key, nil, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	conversationhttp.Register(rt, svc)
	srv := &http.Server{Handler: rt}
	running, stop := context.WithCancel(ctx)
	defer stop()
	served := make(chan error, 1)
	go func() { served <- serve(running, srv, ln, 5*time.Second) }()

	req, _ := http.NewRequest("POST", "http://"+ln.Addr().String()+"/v1/sessions/"+session.ID.String()+"/turns",
		strings.NewReader(`{"request_id":"`+uuid.NewV7().String()+`","words":"Where is the ferry?"}`))
	req.Header.Set("Authorization", "Bearer "+ticket.Reveal())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := bufio.NewReader(resp.Body)
	if first, err := body.ReadString('\n'); err != nil || first != "event: text\n" {
		t.Fatalf("first line %q (%v), want the reply's first text", first, err)
	}
	stop() // as SIGTERM does
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("serve = %v, want a clean stop", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("voice didn't stop")
	}
	rest, _ := io.ReadAll(body)
	saved := turns.All()
	if len(saved) != 1 || saved[0].Status != conversation.TurnLost || saved[0].Reply != "Bearing" {
		t.Fatalf("saved %+v, want the turn saved lost with the text sent", saved)
	}
	if want := "event: lost\ndata: {\"turn_id\":\"" + saved[0].ID.String() + "\",\"seq\":1,\"model\":\"claude-test\"}\n\n"; !strings.HasSuffix(string(rest), want) {
		t.Errorf("the stream ended %q, want %q", rest, want)
	}
}
