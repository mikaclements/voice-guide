package conversation

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/usercontext"
)

const agentPersona = "You keep the lighthouse and answer every call from the sea."

// sessionContextBlocks builds session context as a product might send it. Each
// call builds new slices, as each turn reads them from the session's record.
func sessionContextBlocks() []SessionContextBlock {
	return []SessionContextBlock{
		{Name: "logbook", Content: "Three storms this month."},
		{Name: "chart", Content: "Rocks to the north."},
	}
}

// loadedItems builds user context as a session might have loaded it.
func loadedItems() []usercontext.Item {
	return []usercontext.Item{
		{Kind: usercontext.KindSessionSummary, Content: "Asked about fog signals."},
		{Kind: usercontext.KindProfile, Content: "Prefers short answers."},
	}
}

func TestTheStableLayersStayTheSameOnEveryTurnOfASession(t *testing.T) {
	first := buildPrompt(agentPersona, sessionContextBlocks(), loadedItems(), nil, Turn{Words: "Is the lamp lit?"})
	saved := []Turn{
		{Words: "Is the lamp lit?", Reply: "It is."},
		{ScreenInput: []ScreenInputBlock{{Name: "tide_table", Content: "High water at six."}}, Words: "When is high water?", Reply: "At six."},
	}
	for n := 1; n <= len(saved); n++ {
		later := buildPrompt(agentPersona, sessionContextBlocks(), loadedItems(), saved[:n], Turn{Words: "And the fog?"})
		if !slices.Equal(later.StableLayers, first.StableLayers) {
			t.Errorf("turn %d's stable layers = %q, want turn 1's, byte for byte: %q", n+1, later.StableLayers, first.StableLayers)
		}
	}
}

func TestTheStableLayersAreTheSameWhenBuiltTwiceFromTheSameValues(t *testing.T) {
	one := buildPrompt(agentPersona, sessionContextBlocks(), loadedItems(), nil, Turn{Words: "Is the lamp lit?"})
	other := buildPrompt(agentPersona, sessionContextBlocks(), loadedItems(), nil, Turn{Words: "Is the lamp lit?"})
	if !slices.Equal(one.StableLayers, other.StableLayers) {
		t.Errorf("stable layers = %q, then %q; want the same bytes, as another instance would build them", one.StableLayers, other.StableLayers)
	}
}

func TestThePersonaComesFirstThenTheUserThenTheConversationThenTheNewWords(t *testing.T) {
	saved := []Turn{{Words: "Is the lamp lit?", Reply: "It is."}}
	got := buildPrompt(agentPersona, sessionContextBlocks()[:1], nil, saved, Turn{ScreenInput: []ScreenInputBlock{{Name: "tide_table", Content: "High water at six."}}, Words: "When is high water?"})
	want := Prompt{
		StableLayers: []string{agentPersona, sessionContextIntro + "\n\n<logbook>\nThree storms this month.\n</logbook>"},
		Conversation: []Message{{Role: RoleUser, Text: "Is the lamp lit?"}, {Role: RoleAgent, Text: "It is."}},
		NewWords:     "<tide_table>\nHigh water at six.\n</tide_table>\n\nWhen is high water?",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("prompt =\n%#v\nwant\n%#v", got, want)
	}
}

func TestTheSecondLayerHoldsWhatTheSessionWasGivenAboutTheUser(t *testing.T) {
	sessionPart := sessionContextIntro + "\n\n<logbook>\nThree storms this month.\n</logbook>\n\n<chart>\nRocks to the north.\n</chart>"
	userPart := userContextIntro + "\n\n<session_summary>\nAsked about fog signals.\n</session_summary>\n\n<profile>\nPrefers short answers.\n</profile>"
	tests := map[string]struct {
		sessionContext []SessionContextBlock
		userContext    []usercontext.Item
		want           []string
	}{
		"nothing, so no second layer": {sessionContext: []SessionContextBlock{}, userContext: []usercontext.Item{}, want: []string{agentPersona}},
		"session context only":        {sessionContext: sessionContextBlocks(), want: []string{agentPersona, sessionPart}},
		"user context only":           {userContext: loadedItems(), want: []string{agentPersona, userPart}},
		"both, session context first": {sessionContext: sessionContextBlocks(), userContext: loadedItems(), want: []string{agentPersona, sessionPart + "\n\n" + userPart}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := buildPrompt(agentPersona, tc.sessionContext, tc.userContext, nil, Turn{Words: "Is the lamp lit?"}).StableLayers
			if !slices.Equal(got, tc.want) {
				t.Errorf("stable layers = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestASavedReplyComesBackByteForByte(t *testing.T) {
	reply := "  **Bearing 040.**\n\n- Check the lamp\n- Log the storm\n\n\n"
	got := buildPrompt(agentPersona, nil, nil, []Turn{{Words: "Where is the ferry?", Reply: reply}}, Turn{Words: "Thanks."})
	if text := got.Conversation[1].Text; text != reply {
		t.Errorf("the reply in the conversation = %q, want it exactly as saved: %q", text, reply)
	}
}

func TestATurnWithoutAReplyLeavesOutTheAgentsMessageButKeepsTheUsers(t *testing.T) {
	saved := []Turn{{Words: "Is the lamp lit?", Status: TurnLost}, {Words: "Is the lamp lit now?", Reply: "It is."}}
	got := buildPrompt(agentPersona, nil, nil, saved, Turn{Words: "Thanks."}).Conversation
	want := []Message{{Role: RoleUser, Text: "Is the lamp lit?"}, {Role: RoleUser, Text: "Is the lamp lit now?"}, {Role: RoleAgent, Text: "It is."}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("conversation = %q, want %q", got, want)
	}
}

func TestATurnIsRenderedInTheConversationAsItWasSentAsNewWords(t *testing.T) {
	turn := Turn{
		ScreenInput: []ScreenInputBlock{{Name: "tide_table", Content: "High water at six."}, {Name: "weather", Content: "Fog rolling in."}},
		Words:       "Should I sound the horn?",
	}
	sent := buildPrompt(agentPersona, nil, nil, nil, turn).NewWords

	turn.Reply = "Yes, every two minutes."
	got := buildPrompt(agentPersona, nil, nil, []Turn{turn}, Turn{Words: "Done."}).Conversation[0].Text
	if got != sent {
		t.Errorf("the turn in the conversation = %q, want what was sent as its new words: %q", got, sent)
	}
}

func TestACutReplyEndsWhereTheUserStoppedHearingItAndTheirNextMessageSaysSo(t *testing.T) {
	reply := "Tell me about a time you led a team. What was the hardest call you made, and why?"
	heard := strings.Index(reply, " call")
	saved := []Turn{
		{Words: "Ready.", Reply: reply, Status: TurnCut, Heard: heard},
		{Words: "Wait, which team?", Reply: "Any team you led.", Status: TurnAnswered},
	}
	got := buildPrompt(agentPersona, nil, nil, saved, Turn{Words: "Thanks."}).Conversation
	want := []Message{
		{Role: RoleUser, Text: "Ready."},
		{Role: RoleAgent, Text: "Tell me about a time you led a team. What was the hardest—"},
		{Role: RoleUser, Text: interruptedNote + "\n\nWait, which team?"},
		{Role: RoleAgent, Text: "Any team you led."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("conversation = %q, want %q", got, want)
	}
}

func TestAReplyCutBeforeAnythingWasHeardLeavesOutTheAgentsMessage(t *testing.T) {
	saved := []Turn{{Words: "Ready.", Reply: "Tell me about a time.", Status: TurnCut, Heard: 0}}
	got := buildPrompt(agentPersona, nil, nil, saved, Turn{Words: "Actually, one thing first."})
	if want := []Message{{Role: RoleUser, Text: "Ready."}}; !reflect.DeepEqual(got.Conversation, want) {
		t.Errorf("conversation = %q, want only the user's message: %q", got.Conversation, want)
	}
	if want := interruptedSilentNote + "\n\nActually, one thing first."; got.NewWords != want {
		t.Errorf("new words = %q, want them opened by the note that nothing was said: %q", got.NewWords, want)
	}
}

func TestTheNoteComesBeforeTheScreenInput(t *testing.T) {
	saved := []Turn{{Words: "Ready.", Reply: "Tell me.", Status: TurnCut, Heard: 4}}
	next := Turn{ScreenInput: []ScreenInputBlock{{Name: "weather", Content: "Fog."}}, Words: "Hold on."}
	got := buildPrompt(agentPersona, nil, nil, saved, next).NewWords
	if want := interruptedNote + "\n\n<weather>\nFog.\n</weather>\n\nHold on."; got != want {
		t.Errorf("new words = %q, want %q", got, want)
	}
}

func TestTheAgentLayerIsThePersonaAloneWhenNothingIsGraded(t *testing.T) {
	a := agents.Agent{Persona: agentPersona + "\n", Summary: "Note the ships.", SummaryModel: "claude-opus-5-5"}
	if got := agentLayer(a); got != agentPersona+"\n" {
		t.Errorf("agent layer = %q, want the persona exactly as written", got)
	}
}

func TestTheAgentLayerHoldsTheCriteriaAndTheScaleAfterThePersona(t *testing.T) {
	a := agents.Agent{Persona: agentPersona + "\n", Summary: "Note the ships.", Grading: agents.Grading{
		Lowest: 1, Highest: 5,
		Levels:   []agents.Level{{Score: 1, Means: "Lost a ship."}, {Score: 5, Means: "Guided every ship home."}},
		Criteria: []agents.Criterion{{Name: "fog_calls", LooksFor: "Answers every call in the fog."}, {Name: "lamp_care", LooksFor: "Keeps the lamp lit."}},
	}}
	want := agentPersona + "\n\n" +
		"After this session ends, it is graded on the criteria below, each scored on the scale after them. Steer the conversation so the user has the chance to show each one.\n\n" +
		"<criteria>\n<fog_calls>\nAnswers every call in the fog.\n</fog_calls>\n<lamp_care>\nKeeps the lamp lit.\n</lamp_care>\n</criteria>\n\n" +
		"<scale>\nFrom 1, the lowest, to 5, the highest.\n1: Lost a ship.\n5: Guided every ship home.\n</scale>"
	if got := agentLayer(a); got != want {
		t.Errorf("agent layer =\n%s\nwant\n%s", got, want)
	}
	a.Grading.Levels = nil
	if got := agentLayer(a); !strings.HasSuffix(got, "<scale>\nFrom 1, the lowest, to 5, the highest.\n</scale>") {
		t.Errorf("agent layer without levels ends %q, want the scale's ends alone", got[len(got)-60:])
	}
}
