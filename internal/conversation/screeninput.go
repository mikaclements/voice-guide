package conversation

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mikaclements/voice-guide/internal/agents"
)

// ScreenInputBlock is one named piece of screen input: what the user is doing in
// the product right now, sent with a turn.
type ScreenInputBlock struct {
	Name    string
	Content string
}

// ScreenInputError reports screen input that can't be used, naming each
// problem, so the product can fix its request.
type ScreenInputError struct {
	BadNames, Repeated, Empty, Unstorable []string
}

func (e *ScreenInputError) Error() string {
	var parts []string
	for _, p := range []struct {
		what  string
		names []string
	}{{"names that aren't lower-case letters, digits and _, at most 32", e.BadNames}, {"sent twice", e.Repeated}, {"empty", e.Empty}, {"holding a NUL character, which can't be stored", e.Unstorable}} {
		if len(p.names) > 0 {
			parts = append(parts, p.what+": "+quoted(p.names))
		}
	}
	return "screen input can't be used: " + strings.Join(parts, "; ")
}

// ErrScreenInputTooLarge reports screen input over the character limit.
var ErrScreenInputTooLarge = errors.New("screen input is over the limit")

// checkScreenInput refuses blocks with a badly formed or repeated name, empty
// or unstorable content, or over limit characters in all.
func checkScreenInput(blocks []ScreenInputBlock, limit int) error {
	var e ScreenInputError
	sent := make(map[string]bool, len(blocks))
	chars := 0
	for _, b := range blocks {
		switch {
		case !agents.IsBlockName(b.Name):
			e.BadNames = append(e.BadNames, b.Name)
		case sent[b.Name]:
			e.Repeated = append(e.Repeated, b.Name)
		case b.Content == "":
			e.Empty = append(e.Empty, b.Name)
		case strings.ContainsRune(b.Content, 0): // PostgreSQL's jsonb refuses it
			e.Unstorable = append(e.Unstorable, b.Name)
		}
		sent[b.Name] = true
		chars += utf8.RuneCountInString(b.Content)
	}
	if len(e.BadNames)+len(e.Repeated)+len(e.Empty)+len(e.Unstorable) > 0 {
		return &e
	}
	if chars > limit {
		return fmt.Errorf("%w: %d characters, at most %d", ErrScreenInputTooLarge, chars, limit)
	}
	return nil
}
