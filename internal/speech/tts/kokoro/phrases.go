package kokoro

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// minWords is the fewest words a phrase holds, unless the reply ends sooner. Kokoro's time to
// first audio is mostly fixed (measured on an M3 Pro: 1 word 0.48s, 4 words 0.58s, 8 words
// 0.75s), so a shorter phrase starts little sooner and sounds clipped.
const minWords = 4

// joining words open a clause, so the first phrase may end before one without cutting a clause.
// Only words that almost always do: "that", "after" or "before" are as often a determiner or a
// preposition ("walk me through that decision", "after that"), and "and", "but" or "so" join
// words as often as clauses ("your team and your manager"); after a comma the comma cuts anyway.
var joining = map[string]bool{"when": true, "because": true, "if": true}

// binding words make one unit with a joining word after them ("even if", "as if", "only when"),
// so the first phrase doesn't end between the two.
var binding = map[string]bool{"even": true, "as": true, "only": true}

// phrases cuts a reply's text into phrases as it streams. The first phrase ends at the first
// natural break (a comma, semicolon, colon, dash or sentence end, and with joiningWords also
// before a joining word), so speech starts soon; later phrases are whole sentences, which Kokoro makes while the
// phrase before plays. No phrase is cut inside a clause.
type phrases struct {
	pending string // the reply's text not yet in a phrase
	before  int    // the bytes of the reply before pending
	cut     int    // phrases cut so far
	// joiningWords also ends the first phrase before a joining word.
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
		out = append(out, cutPhrase{strings.TrimSpace(p.pending[:end]), p.before + end})
		p.pending = p.pending[end:]
		p.before += end
		p.cut++
	}
}

// next returns where the next phrase in pending ends, or 0 if it hasn't ended yet.
func (p *phrases) next(final bool) int {
	words := wordsIn(p.pending, final)
	for i := minWords - 1; i < len(words); i++ {
		w := words[i]
		if sentenceEnd(w.text) {
			return w.end
		}
		if p.cut > 0 {
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

// digitDash reports whether the dash at s[dash:end] has a digit on both sides, in a word that
// starts at start.
func digitDash(s string, start, dash, end int) bool {
	before, _ := utf8.DecodeLastRuneInString(s[start:dash])
	after, _ := utf8.DecodeRuneInString(s[end:])
	return unicode.IsDigit(before) && unicode.IsDigit(after)
}

func sentenceEnd(w string) bool {
	w = strings.TrimRight(w, `"'”’)]`)
	return strings.HasSuffix(w, ".") || strings.HasSuffix(w, "?") || strings.HasSuffix(w, "!") ||
		strings.HasSuffix(w, "…")
}

func clauseEnd(w string) bool {
	return w == "-" || strings.HasSuffix(w, ",") || strings.HasSuffix(w, ";") || strings.HasSuffix(w, ":") ||
		strings.HasSuffix(w, "—") || strings.HasSuffix(w, "–")
}
