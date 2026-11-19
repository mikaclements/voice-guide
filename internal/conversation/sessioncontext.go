package conversation

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// SessionContextBlock is one named piece of session context: something the
// product already knows about the user, such as their resume, sent when it
// starts a session.
type SessionContextBlock struct {
	Name    string
	Content string
}

// SessionContextMismatchError reports session context that doesn't match what
// the agent's version declares, naming each problem, so the product can fix
// its request.
type SessionContextMismatchError struct {
	Missing, Unknown, Repeated, Empty, Unstorable []string
}

func (e *SessionContextMismatchError) Error() string {
	var parts []string
	for _, p := range []struct {
		what  string
		names []string
	}{{"missing", e.Missing}, {"not expected by this agent", e.Unknown}, {"sent twice", e.Repeated}, {"empty", e.Empty}, {"holding a NUL character, which can't be stored", e.Unstorable}} {
		if len(p.names) > 0 {
			parts = append(parts, p.what+": "+quoted(p.names))
		}
	}
	return "session context doesn't match what the agent expects: " + strings.Join(parts, "; ")
}

// ErrSessionContextTooLarge reports session context over the character limit.
var ErrSessionContextTooLarge = errors.New("session context is over the limit")

// checkSessionContext refuses session context that isn't exactly the names
// the version declares, each once, not empty and storable, or that is over
// limit characters in all, which also bounds each block.
func checkSessionContext(blocks []SessionContextBlock, declared []string, limit int) error {
	var e SessionContextMismatchError
	sent := make(map[string]bool, len(blocks))
	chars := 0
	for _, b := range blocks {
		switch {
		case sent[b.Name]:
			e.Repeated = append(e.Repeated, b.Name)
		case !slices.Contains(declared, b.Name):
			e.Unknown = append(e.Unknown, b.Name)
		case b.Content == "":
			e.Empty = append(e.Empty, b.Name)
		case strings.ContainsRune(b.Content, 0): // PostgreSQL's jsonb refuses it
			e.Unstorable = append(e.Unstorable, b.Name)
		}
		sent[b.Name] = true
		chars += utf8.RuneCountInString(b.Content)
	}
	for _, name := range declared {
		if !sent[name] {
			e.Missing = append(e.Missing, name)
		}
	}
	if len(e.Missing)+len(e.Unknown)+len(e.Repeated)+len(e.Empty)+len(e.Unstorable) > 0 {
		return &e
	}
	if chars > limit {
		return fmt.Errorf("%w: %d characters, at most %d", ErrSessionContextTooLarge, chars, limit)
	}
	return nil
}

// quoted lists names for a message to the caller. The names come from the
// request, so each is cut short and only the first few are listed: five names
// of up to 40 characters fit one readable line, and a valid name is at most 32.
func quoted(names []string) string {
	const most, longest = 5, 40
	out := make([]string, 0, most+1)
	for _, n := range names[:min(len(names), most)] {
		if utf8.RuneCountInString(n) > longest {
			n = string([]rune(n)[:longest]) + "…"
		}
		out = append(out, fmt.Sprintf("%q", n))
	}
	if len(names) > most {
		out = append(out, fmt.Sprintf("and %d more", len(names)-most))
	}
	return strings.Join(out, ", ")
}
