package conversation_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/conversation"
)

// shipLog is a document only the keeper's summary reads: the agent must never see it while it
// talks.
const shipLog = "The Mary Rose came in late on the ninth."

// keeperWithShipLog is the service over fakes with the keeper, who speaks first, expecting the
// page the user is on as session context, and taking a ship log only its summary reads, its
// summary with data when data is set.
func keeperWithShipLog(t *testing.T, data bool) serviceOverFakes {
	t.Helper()
	h := newServiceOverFakes(t)
	keeper := agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea.", Opens: true,
		SessionContext: []string{"page"}, Summary: instruction, SummaryDocuments: []string{"ship_log"}}
	if data {
		keeper.SummarySchema = json.RawMessage(recipeSchema)
	}
	h.publish(t, keeper)
	return h
}

func withShipLog() []conversation.SessionContextBlock {
	return []conversation.SessionContextBlock{block("page", "Harbour charts"), block("ship_log", shipLog)}
}

func TestAStartMaySendADocumentOnlyTheSummaryReadsOrLeaveItOut(t *testing.T) {
	tests := map[string]struct {
		sent []conversation.SessionContextBlock
		want *conversation.SessionContextMismatchError
	}{
		"sent":                       {withShipLog(), nil},
		"left out":                   {[]conversation.SessionContextBlock{block("page", "Harbour charts")}, nil},
		"one undeclared":             {append(withShipLog(), block("diary", "Saw a whale.")), &conversation.SessionContextMismatchError{Unknown: []string{"diary"}}},
		"the conversation's missing": {[]conversation.SessionContextBlock{block("ship_log", shipLog)}, &conversation.SessionContextMismatchError{Missing: []string{"page"}}},
		"empty":                      {[]conversation.SessionContextBlock{block("page", "Harbour charts"), block("ship_log", "")}, &conversation.SessionContextMismatchError{Empty: []string{"ship_log"}}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			h := keeperWithShipLog(t, false)
			s, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), SessionContext: tt.sent})
			var got *conversation.SessionContextMismatchError
			switch {
			case tt.want == nil && (err != nil || !slices.Equal(s.SessionContext, tt.sent)):
				t.Errorf("StartSession = %+v, %v; want the session context recorded as sent", s.SessionContext, err)
			case tt.want != nil && (!errors.As(err, &got) || !slices.Equal(got.Unknown, tt.want.Unknown) || !slices.Equal(got.Missing, tt.want.Missing) || !slices.Equal(got.Empty, tt.want.Empty)):
				t.Errorf("StartSession = %v, want a mismatch %+v", err, *tt.want)
			}
		})
	}
}

func TestARepeatWithAnotherDocumentOnlyTheSummaryReadsIsRefused(t *testing.T) {
	h := keeperWithShipLog(t, false)
	request := uuid.NewV7()
	if _, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: request, SessionContext: withShipLog()}); err != nil {
		t.Fatalf("first start: %v", err)
	}
	other := []conversation.SessionContextBlock{block("page", "Harbour charts"), block("ship_log", "Nothing came in.")}
	if _, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: request, SessionContext: other}); !errors.Is(err, conversation.ErrRequestReused) {
		t.Errorf("repeat = %v, want ErrRequestReused", err)
	}
}

// asked joins everything a request to the model holds.
func asked(r conversation.ModelRequest) string {
	parts := append(slices.Clone(r.StableLayers), r.NewWords)
	for _, m := range r.Conversation {
		parts = append(parts, m.Text)
	}
	return strings.Join(parts, "\n")
}

func TestADocumentOnlyTheSummaryReadsNeverReachesTheConversationsPrompt(t *testing.T) {
	for name, turn := range map[string]func(uuid.UUID) conversation.TurnRequest{
		"a typed turn": func(s uuid.UUID) conversation.TurnRequest { return turnIn(s, "Is the lamp lit?") },
		"a spoken turn": func(s uuid.UUID) conversation.TurnRequest {
			r := turnIn(s, "Is the lamp lit?")
			r.Hold = true
			return r
		},
		"the opening": openingIn,
	} {
		t.Run(name, func(t *testing.T) {
			h := keeperWithShipLog(t, false)
			s, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), SessionContext: withShipLog()})
			if err != nil {
				t.Fatalf("StartSession: %v", err)
			}
			h.takeTurn(t, turn(s.ID))
			requests, _ := h.model.Requests()
			if got := asked(requests[len(requests)-1]); strings.Contains(got, shipLog) || strings.Contains(got, "ship_log") || !strings.Contains(got, "Harbour charts") {
				t.Errorf("the model was asked with %q; want the page and no trace of the ship log", got)
			}
		})
	}
}

// summarizedWithShipLog runs the summary of a session with the keeper started with the ship log,
// and returns what the summary's call was asked.
func summarizedWithShipLog(t *testing.T, data bool) conversation.ModelRequest {
	t.Helper()
	h := keeperWithShipLog(t, data)
	s, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), SessionContext: withShipLog()})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	h.takeTurn(t, turnIn(s.ID, "Is the lamp lit?"))
	if _, err := h.svc.EndSession(context.Background(), caller, s.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	h.model.Answers = []string{validAnswer}
	if err := h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID}); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	requests, _ := h.model.Requests()
	ask := requests[len(requests)-1]
	if layers := strings.Join(ask.StableLayers, "\n"); strings.Contains(layers, shipLog) || !strings.Contains(layers, "don't restate their content") {
		t.Errorf("asked with layers %q; want no ship log there, and the notes told not to restate documents, which the next session loads", layers)
	}
	return ask
}

func TestThePlainSummaryReadsItsDocumentsInTheRecordBeforeTheTranscript(t *testing.T) {
	ask := summarizedWithShipLog(t, false)
	want := "<documents>\n<ship_log>\n" + shipLog + "\n</ship_log>\n</documents>\n\n<transcript>\n"
	if !strings.HasPrefix(ask.NewWords, want) || strings.Contains(ask.NewWords, "Harbour charts") {
		t.Errorf("asked with %q; want it to open with the ship log alone, then the transcript", ask.NewWords)
	}
}

func TestTheSummaryWithDataReadsItsDocumentsInTheJSONRecord(t *testing.T) {
	ask := summarizedWithShipLog(t, true)
	var record struct {
		Documents []struct{ Name, Content string } `json:"documents"`
		Turns     []struct{ User string }          `json:"turns"`
	}
	if err := json.Unmarshal([]byte(ask.NewWords), &record); err != nil || len(record.Documents) != 1 ||
		record.Documents[0].Name != "ship_log" || record.Documents[0].Content != shipLog || len(record.Turns) != 1 {
		t.Errorf("asked with %q (%v); want the ship log alone among the record's documents, then the turn", ask.NewWords, err)
	}
}

func TestADocumentInBothListsReachesTheAgentAndTheSummary(t *testing.T) {
	h := newServiceOverFakes(t)
	h.publish(t, agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea.",
		SessionContext: []string{"page"}, Summary: instruction, SummaryDocuments: []string{"page"}})
	s, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), SessionContext: []conversation.SessionContextBlock{block("page", "Harbour charts")}})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	h.takeTurn(t, turnIn(s.ID, "Is the lamp lit?"))
	_, _ = h.svc.EndSession(context.Background(), caller, s.ID)
	if err := h.svc.Summarize(context.Background(), workspace, user, conversation.SummaryArgs{Session: s.ID}); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	requests, _ := h.model.Requests()
	if turn, summary := asked(requests[0]), requests[len(requests)-1].NewWords; !strings.Contains(turn, "Harbour charts") || !strings.Contains(summary, "<page>\nHarbour charts\n</page>") {
		t.Errorf("the turn was asked with %q and the summary with %q; want the page in both", turn, summary)
	}
}
