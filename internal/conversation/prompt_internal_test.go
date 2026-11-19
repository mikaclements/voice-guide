package conversation

import (
	"reflect"
	"slices"
	"strings"
	"testing"

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
	want.NewWordsSaved = "(The user's tide_table changed here.)\n\nWhen is high water?"
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

func TestATurnIsRenderedInTheConversationAsItsPromptSaidItWouldBe(t *testing.T) {
	turn := Turn{
		ScreenInput: []ScreenInputBlock{{Name: "tide_table", Content: "High water at six."}, {Name: "weather", Content: "Fog rolling in."}},
		Words:       "Should I sound the horn?",
	}
	saved := buildPrompt(agentPersona, nil, nil, nil, turn).NewWordsSaved

	turn.Reply = "Yes, every two minutes."
	got := buildPrompt(agentPersona, nil, nil, []Turn{turn}, Turn{Words: "Done."}).Conversation[0].Text
	if got != saved {
		t.Errorf("the turn in the conversation = %q, want what its own prompt said it would be: %q", got, saved)
	}
}

func TestASavedTurnsScreenInputIsAMarkerAndTheNewestBlockGoesWithTheNewWords(t *testing.T) {
	saved := []Turn{
		{ScreenInput: []ScreenInputBlock{{Name: "chart", Content: "Rocks to the north."}}, Words: "Which way?", Reply: "South."},
		{Words: "And then?", Reply: "Hold the course."},
	}
	got := buildPrompt(agentPersona, nil, nil, saved, Turn{Words: "How long?"})
	want := []Message{
		{Role: RoleUser, Text: "(The user's chart changed here.)\n\nWhich way?"},
		{Role: RoleAgent, Text: "South."},
		{Role: RoleUser, Text: "And then?"},
		{Role: RoleAgent, Text: "Hold the course."},
	}
	if !reflect.DeepEqual(got.Conversation, want) {
		t.Errorf("conversation = %q, want the chart as a marker on the turn that carried it: %q", got.Conversation, want)
	}
	if want := "<chart>\nRocks to the north.\n</chart>\n\nHow long?"; got.NewWords != want {
		t.Errorf("new words = %q, want the newest chart before them: %q", got.NewWords, want)
	}
	if got.NewWordsSaved != "How long?" {
		t.Errorf("new words as saved = %q, want only the words, since this turn carried no screen input", got.NewWordsSaved)
	}
}

func TestTheNewTurnsOwnBlockWinsOverASavedOne(t *testing.T) {
	saved := []Turn{{ScreenInput: []ScreenInputBlock{{Name: "chart", Content: "Rocks to the north."}}, Words: "Which way?", Reply: "South."}}
	next := Turn{ScreenInput: []ScreenInputBlock{{Name: "chart", Content: "Rocks to the east."}}, Words: "Now?"}
	got := buildPrompt(agentPersona, nil, nil, saved, next)
	if want := "<chart>\nRocks to the east.\n</chart>\n\nNow?"; got.NewWords != want {
		t.Errorf("new words = %q, want only the new turn's chart: %q", got.NewWords, want)
	}
	if want := "(The user's chart changed here.)\n\nNow?"; got.NewWordsSaved != want {
		t.Errorf("new words as saved = %q, want the marker: %q", got.NewWordsSaved, want)
	}
}

func TestTwoNamesChangingAtDifferentTurnsEachGoNewestInTheirFirstOrder(t *testing.T) {
	saved := []Turn{
		{ScreenInput: []ScreenInputBlock{{Name: "chart", Content: "Rocks to the north."}}, Words: "Which way?", Reply: "South."},
		{ScreenInput: []ScreenInputBlock{{Name: "logbook", Content: "Fog at dawn."}}, Words: "Noted?", Reply: "Noted."},
		{ScreenInput: []ScreenInputBlock{{Name: "chart", Content: "Rocks to the east."}}, Words: "Moved?", Reply: "Yes."},
	}
	got := buildPrompt(agentPersona, nil, nil, saved, Turn{Words: "Ready."})
	for i, want := range []string{
		"(The user's chart changed here.)\n\nWhich way?",
		"(The user's logbook changed here.)\n\nNoted?",
		"(The user's chart changed here.)\n\nMoved?",
	} {
		if text := got.Conversation[2*i].Text; text != want {
			t.Errorf("turn %d's message = %q, want %q", i+1, text, want)
		}
	}
	want := "<chart>\nRocks to the east.\n</chart>\n\n<logbook>\nFog at dawn.\n</logbook>\n\nReady."
	if got.NewWords != want {
		t.Errorf("new words = %q, want the newest of each name, in the order they first came: %q", got.NewWords, want)
	}
}

func TestABoardChangingEveryTurnLeavesTheEarlierConversationByteForByte(t *testing.T) {
	var saved []Turn
	var before []Message
	for _, chart := range []string{"Rocks to the north.", "Rocks to the east.", "Rocks to the south."} {
		next := Turn{ScreenInput: []ScreenInputBlock{{Name: "chart", Content: chart}}, Words: "And now?"}
		got := buildPrompt(agentPersona, nil, nil, saved, next)
		if !slices.Equal(got.Conversation[:len(before)], before) {
			t.Errorf("after %d turns the conversation = %q, want the one before it kept byte for byte: %q", len(saved), got.Conversation, before)
		}
		next.Reply = "Steer clear."
		saved, before = append(saved, next), got.Conversation
	}
}

func TestTheSameStoredTurnsGiveTheSamePrompt(t *testing.T) {
	stored := func() []Turn {
		return []Turn{
			{ScreenInput: []ScreenInputBlock{{Name: "chart", Content: "Rocks to the north."}, {Name: "logbook", Content: "Fog."}}, Words: "Which way?", Reply: "South."},
			{ScreenInput: []ScreenInputBlock{{Name: "chart", Content: "Rocks to the east."}}, Words: "Moved?", Reply: "Yes."},
		}
	}
	next := func() Turn {
		return Turn{ScreenInput: []ScreenInputBlock{{Name: "logbook", Content: "Clear."}}, Words: "Go?"}
	}
	one := buildPrompt(agentPersona, nil, nil, stored(), next())
	other := buildPrompt(agentPersona, nil, nil, stored(), next())
	if !reflect.DeepEqual(one, other) {
		t.Errorf("prompt =\n%#v\nthen\n%#v\nwant the same bytes, as another instance would build them", one, other)
	}
}

func TestASessionWithoutScreenInputHasNoMarkersAndNothingBeforeTheWords(t *testing.T) {
	saved := []Turn{{Words: "Is the lamp lit?", Reply: "It is."}}
	got := buildPrompt(agentPersona, nil, nil, saved, Turn{Words: "Thanks."})
	want := Prompt{
		StableLayers:  []string{agentPersona},
		Conversation:  []Message{{Role: RoleUser, Text: "Is the lamp lit?"}, {Role: RoleAgent, Text: "It is."}},
		NewWords:      "Thanks.",
		NewWordsSaved: "Thanks.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("prompt =\n%#v\nwant\n%#v", got, want)
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

func TestASummaryMarksEachScreenChangeAndHoldsTheNewestBlockOfEachNameOnce(t *testing.T) {
	turns := []Turn{
		{ScreenInput: []ScreenInputBlock{{Name: "chart", Content: "Rocks to the north."}}, Words: "Which way?", Reply: "South."},
		{ScreenInput: []ScreenInputBlock{{Name: "logbook", Content: "Fog at dawn."}}, Words: "Noted?", Reply: "Noted."},
		{ScreenInput: []ScreenInputBlock{{Name: "chart", Content: "Rocks to the east."}}, Words: "Moved?", Reply: "Yes."},
	}
	got := summaryPrompt("Note the course.", turns).NewWords
	want := "<transcript>\n" +
		"User: (The user's chart changed here.)\n\nWhich way?\n\nAgent: South.\n\n" +
		"User: (The user's logbook changed here.)\n\nNoted?\n\nAgent: Noted.\n\n" +
		"User: (The user's chart changed here.)\n\nMoved?\n\nAgent: Yes.\n\n" +
		"</transcript>\n\n" +
		screenAtEnd + "\n\n<chart>\nRocks to the east.\n</chart>\n\n<logbook>\nFog at dawn.\n</logbook>\n\n" +
		"<instructions>\nNote the course.\n</instructions>"
	if got != want {
		t.Errorf("summary's ask =\n%s\nwant\n%s", got, want)
	}
}
