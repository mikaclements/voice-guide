package conversation

import (
	"slices"
	"strings"
	"unicode"

	"github.com/mikaclements/voice-guide/internal/usercontext"
)

// The lines that introduce what a session was given about the user, so the
// model can tell where each part came from. They are fixed: a byte that
// changed between turns would make the model read the session's layer again
// instead of from its cache.
const (
	sessionContextIntro = "What the product knows about the user, sent when this session started:"
	userContextIntro    = "Learned about the user in earlier conversations:"
)

// interruptedNote opens the user's message after a reply they cut short, so
// the model knows its last words stopped because the user cut in, not because
// it stopped on its own. It sits in the user's message, not the agent's, so
// the model doesn't learn to write such notes in its replies.
const interruptedNote = "(The user interrupted you; your last reply ends where they stopped hearing it.)"

// interruptedSilentNote opens the user's message after they cut in before the
// agent said anything, so the model doesn't take its earlier reply as the one
// cut short, nor read the silence as its own choice.
const interruptedSilentNote = "(The user interrupted you before you said anything.)"

// openingCue stands in the user's place when the agent opens the session, so
// the model writes its greeting from its instructions and what the session
// was given. Every later prompt holds it again as the session's first user
// message, byte for byte, so the opening's request warms the cache for the
// turns after it.
const openingCue = "(The user has just joined and hasn't said anything yet. Open the conversation as your instructions say.)"

// screenChanged stands in a saved turn's message for each screen input block
// it carried, in place of the content. The line is the same bytes every turn,
// so a board that changes every turn never changes the conversation before it,
// which the model reads from its cache; the newest block of each name goes
// whole in the new message instead.
func screenChanged(name string) string { return "(The user's " + name + " changed here.)" }

// Prompt is what the model is sent for one turn.
type Prompt struct {
	// StableLayers are the same, byte for byte, on every turn of a session, so
	// the model can read them from its cache: the agent version's persona, then
	// what the session was given about the user, if it was given anything.
	StableLayers []string
	// Conversation is the session's earlier turns, oldest first.
	Conversation []Message
	// NewWords is what the user sent in this turn, opened by the newest screen
	// input block of each name the session has had.
	NewWords string
	// NewWordsSaved is this turn as the conversation of later turns holds it:
	// its screen input blocks as markers. A provider that keeps the
	// conversation between turns records this, so it matches the next request.
	NewWordsSaved string
}

// Role says who a message in the conversation is from.
type Role string

// The two sides of a conversation.
const (
	RoleUser  Role = "user"
	RoleAgent Role = "agent"
)

// Message is one side of a turn as the model reads it.
type Message struct {
	Role Role
	Text string
}

// buildPrompt renders the prompt for the turn next from the agent version's
// persona, what the session was given about the user in the order recorded,
// and the turns saved before it. It depends on nothing but its arguments, so
// every instance builds the same bytes for the same session.
func buildPrompt(persona string, sessionContext []SessionContextBlock, userContext []usercontext.Item, saved []Turn, next Turn) Prompt {
	p := Prompt{StableLayers: []string{persona}, Conversation: make([]Message, 0, 2*len(saved))}
	if about := aboutTheUser(sessionContext, userContext); about != "" {
		p.StableLayers = append(p.StableLayers, about)
	}
	note := ""
	var screen []ScreenInputBlock
	for _, t := range saved {
		p.Conversation = append(p.Conversation, Message{Role: RoleUser, Text: userMessage(t, note, markers(t.ScreenInput))})
		screen = newest(screen, t.ScreenInput)
		reply := agentMessage(t)
		if reply != "" { // a lost or cut turn may have none, and an empty message tells the model nothing
			p.Conversation = append(p.Conversation, Message{Role: RoleAgent, Text: reply})
		}
		note = noteAfter(t, reply)
	}
	p.NewWords = userMessage(next, note, whole(newest(screen, next.ScreenInput)))
	p.NewWordsSaved = userMessage(next, note, markers(next.ScreenInput))
	return p
}

// newest returns the newest block of each name once blocks have arrived after
// those in screen, a name keeping its place from when it first came, so the
// order is the same on every turn. Like append, it may change screen in place.
func newest(screen, blocks []ScreenInputBlock) []ScreenInputBlock {
	for _, b := range blocks {
		if i := slices.IndexFunc(screen, func(o ScreenInputBlock) bool { return o.Name == b.Name }); i >= 0 {
			screen[i] = b
		} else {
			screen = append(screen, b)
		}
	}
	return screen
}

// whole renders screen input blocks with their content, each tagged with its
// name.
func whole(blocks []ScreenInputBlock) []string {
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, tagged(b.Name, b.Content))
	}
	return out
}

// markers renders a saved turn's screen input as the line marking each change.
func markers(blocks []ScreenInputBlock) []string {
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, screenChanged(b.Name))
	}
	return out
}

// noteAfter is the note opening the user's message after turn t, whose reply
// rendered as reply: none unless the user cut it short.
func noteAfter(t Turn, reply string) string {
	switch {
	case t.Status != TurnCut:
		return ""
	case reply == "":
		return interruptedSilentNote
	default:
		return interruptedNote
	}
}

// agentMessage renders the reply as the user heard it: whole and byte for
// byte, or, when they cut it short, up to where they stopped hearing it, with
// a dash for the break.
func agentMessage(t Turn) string {
	if t.Status != TurnCut {
		return t.Reply
	}
	if heard := strings.TrimRightFunc(t.HeardReply(), unicode.IsSpace); heard != "" {
		return heard + "—"
	}
	return ""
}

// aboutTheUser renders the session context the product sent, then the user
// context the session loaded, each behind its own line; empty when the
// session was given neither.
func aboutTheUser(sessionContext []SessionContextBlock, userContext []usercontext.Item) string {
	var parts []string
	if len(sessionContext) > 0 {
		parts = append(parts, sessionContextIntro)
		for _, b := range sessionContext {
			parts = append(parts, tagged(b.Name, b.Content))
		}
	}
	if len(userContext) > 0 {
		parts = append(parts, userContextIntro)
		for _, it := range userContext {
			parts = append(parts, contextBlock(it))
		}
	}
	return strings.Join(parts, "\n\n")
}

// userMessage renders what the user sent in a turn: the note that they cut
// the reply before it short, if any, then the screen input as rendered, so
// the words read as being about it, then the words, or the cue for the
// agent's opening.
func userMessage(t Turn, note string, screen []string) string {
	words := t.Words
	if t.Opening {
		words = openingCue
	}
	parts := make([]string, 0, len(screen)+2)
	if note != "" {
		parts = append(parts, note)
	}
	parts = append(parts, screen...)
	return strings.Join(append(parts, words), "\n\n")
}

// contextBlock renders an item user context loaded: a session summary says
// when its session ended, so the model can tell the last session from the
// ones before.
func contextBlock(it usercontext.Item) string {
	if it.SessionEnded.IsZero() {
		return tagged(it.Kind, it.Content)
	}
	ended := it.SessionEnded.UTC().Format("2006-01-02 15:04 UTC")
	return "<" + it.Kind + ` ended="` + ended + `">` + "\n" + it.Content + "\n</" + it.Kind + ">"
}

func tagged(name, content string) string {
	return "<" + name + ">\n" + content + "\n</" + name + ">"
}
