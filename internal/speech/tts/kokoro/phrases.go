package kokoro

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// minWords is the fewest words a phrase holds, unless the reply ends sooner or an early phrase
// is a whole sentence. Kokoro speaks each phrase with its own rise and fall and a pause after it,
// so "So," or "For example:" alone would break its sentence in two; and Kokoro's time to first
// audio is mostly fixed (measured on an M3 Pro: 1 word 0.48s, 4 words 0.58s, 8 words 0.75s in the
// container), so a shorter phrase starts little sooner. A short early sentence ("Got it.") is
// the exception: a speaker pauses after it anyway, and it plays while the model writes on.
const minWords = 4

// joining words open a clause, so an early phrase may end before one without cutting a clause.
// Only words that almost always do: "that", "after" or "before" are as often a determiner or a
// preposition ("walk me through that decision", "after that"), and "and", "but" or "so" join
// words as often as clauses ("your team and your manager"); after a comma the comma cuts anyway.
var joining = map[string]bool{"when": true, "because": true, "if": true}

// binding words make one unit with a joining word after them ("even if", "as if", "only when"),
// so an early phrase doesn't end between the two.
var binding = map[string]bool{"even": true, "as": true, "only": true}

// titles stand before a name, so their period ends no sentence ("Dr. Lee").
var titles = map[string]bool{"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true, "st": true}

// phrases cuts a reply's text into phrases as it streams. Until minWords words have been cut, an
// early phrase ends at the first sentence end, however short, as long as it holds a letter, or at
// the first other natural break (a comma, semicolon, colon or dash, and with joiningWords also
// before a joining word) once it holds minWords words, so speech starts soon, and the phrase after
// a short first sentence is ready before that sentence has played; later phrases are whole
// sentences, which Kokoro makes while the phrase before plays. No phrase is cut inside a clause.
type phrases struct {
	pending string // the reply's text not yet in a phrase
	before  int    // the bytes of the reply before pending
	spoken  int    // words in the phrases cut so far
	// joiningWords also ends an early phrase before a joining word.
	joiningWords bool
}

// cutPhrase is a phrase's text and where it ends in the reply's text, in bytes.
type cutPhrase struct {
	text string
	end  int
}

// add takes the next piece of the reply's text and returns the phrases it completes.
func (p *phrases) add(text string) []cutPhrase {
	p.pending += text
	return p.take(false)
}

// end returns the phrases left once the reply has ended.
func (p *phrases) end() []cutPhrase {
	out := p.take(true)
	if rest := strings.TrimRightFunc(p.pending, unicode.IsSpace); strings.TrimSpace(rest) != "" {
		out = append(out, cutPhrase{strings.TrimSpace(rest), p.before + len(rest)})
	}
	p.pending = ""
	return out
}

func (p *phrases) take(final bool) []cutPhrase {
	var out []cutPhrase
	for {
		end := p.next(final)
		if end == 0 {
			return out
		}
		text := strings.TrimSpace(p.pending[:end])
		out = append(out, cutPhrase{text, p.before + end})
		p.pending = p.pending[end:]
		p.before += end
		p.spoken += len(wordsIn(text, true))
	}
}

// next returns where the next phrase in pending ends, or 0 if it hasn't ended yet.
func (p *phrases) next(final bool) int {
	words := wordsIn(p.pending, final || endsSentence(p.pending))
	early := p.spoken < minWords
	for i, w := range words {
		short := i < minWords-1
		// A short sentence holds a letter, so an ellipsis or a list's number never plays alone.
		if sentenceEnd(w.text) && (!short || early && strings.IndexFunc(p.pending[:w.end], unicode.IsLetter) >= 0) {
			return w.end
		}
		if !early || short {
			continue
		}
		if clauseEnd(w.text) || p.joiningWords && i+1 < len(words) && joining[strings.ToLower(words[i+1].text)] && !binding[strings.ToLower(w.text)] {
			return w.end
		}
	}
	return 0
}

type word struct {
	text string
	end  int // where the word ends in the text
}

// wordsIn returns the words of s that are whole: followed by a space, or ending in a dash
// followed by anything, since the model writes dashes between words without spaces; a dash
// between digits ("2020–2023") stays inside its word. The last word may go on in the next piece
// of text, so it counts only once the reply is final.
func wordsIn(s string, final bool) []word {
	var words []word
	start := -1
	for i, r := range s {
		if unicode.IsSpace(r) {
			if start >= 0 {
				words = append(words, word{s[start:i], i})
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
		end := i + utf8.RuneLen(r)
		if (r == '—' || r == '–') && end < len(s) && !digitDash(s, start, i, end) {
			words = append(words, word{s[start:end], end})
			start = -1
		}
	}
	if start >= 0 && final {
		words = append(words, word{s[start:], len(s)})
	}
	return words
}

// endsSentence reports whether s ends a sentence at its very end, with no space after it yet: the
// model's text arrives in bursts, and one ending at "Okay." would otherwise wait for the next. A
// period after a digit or a single letter may still go on ("2.5", "e.g.").
func endsSentence(s string) bool {
	last, size := utf8.DecodeLastRuneInString(s)
	switch last {
	case '?', '!', '…':
		return true
	case '.':
		before, n := utf8.DecodeLastRuneInString(s[:len(s)-size])
		earlier, _ := utf8.DecodeLastRuneInString(s[:len(s)-size-n])
		return unicode.IsLetter(before) && unicode.IsLetter(earlier)
	}
	return false
}

// digitDash reports whether the dash at s[dash:end] has a digit on both sides, in a word that
// starts at start.
func digitDash(s string, start, dash, end int) bool {
	before, _ := utf8.DecodeLastRuneInString(s[start:dash])
	after, _ := utf8.DecodeRuneInString(s[end:])
	return unicode.IsDigit(before) && unicode.IsDigit(after)
}

// sentenceEnd reports whether a word ends a sentence. An abbreviation's period doesn't: one with a
// period inside ("U.S.", "e.g.") or a title's.
func sentenceEnd(w string) bool {
	w = strings.TrimRight(w, `"'”’)]`)
	if strings.HasSuffix(w, "?") || strings.HasSuffix(w, "!") || strings.HasSuffix(w, "…") {
		return true
	}
	stem, found := strings.CutSuffix(w, ".")
	stem = strings.TrimRight(stem, ".") // an ellipsis of full stops
	return found && !strings.Contains(stem, ".") && !titles[strings.ToLower(stem)]
}

func clauseEnd(w string) bool {
	return w == "-" || strings.HasSuffix(w, ",") || strings.HasSuffix(w, ";") || strings.HasSuffix(w, ":") ||
		strings.HasSuffix(w, "—") || strings.HasSuffix(w, "–")
}
