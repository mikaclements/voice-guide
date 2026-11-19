package kokoro

import (
	"reflect"
	"strings"
	"testing"
)

// cutCase is a reply streamed as pieces and the phrases it should be cut into.
type cutCase struct {
	name   string
	pieces []string // the reply's text as the model streams it
	want   []string
}

func TestARepliesTextIsCutIntoPhrasesAtNaturalBreaks(t *testing.T) {
	checkCuts(t, false, []cutCase{
		{"by default the first phrase ends only at punctuation",
			[]string{"Tell me about a time when you disagreed with your manager."},
			[]string{"Tell me about a time when you disagreed with your manager."}},
		{"a phrase ends at a sentence end",
			[]string{"Walk me through your most recent project at work. What did you build, and why did it matter?"},
			[]string{"Walk me through your most recent project at work.", "What did you build, and why did it matter?"}},
		{"a sentence under four words joins the next",
			[]string{"Walk me through your most recent project at work. Great. What did you build?"},
			[]string{"Walk me through your most recent project at work.", "Great. What did you build?"}},
		{"a sentence ends inside closing quotes",
			[]string{`You said "it shipped late." Why was that, do you think?`},
			[]string{`You said "it shipped late."`, "Why was that, do you think?"}},
		{"a word split across pieces counts once it is whole",
			[]string{"We spent about 2.", "5 million on it."},
			[]string{"We spent about 2.5 million on it."}},
		{"text left when the reply ends is the last phrase",
			[]string{"Walk me through your work. Then tell me"},
			[]string{"Walk me through your work.", "Then tell me"}},
		{"spaces alone make no phrase",
			[]string{"  ", "\n"},
			nil},
		{"the first phrase ends at a comma once it holds four words",
			[]string{"Thanks for sharing that, now tell me more."},
			[]string{"Thanks for sharing that,", "now tell me more."}},
		{"the first phrase ends at a colon",
			[]string{"Here is my question: what did you change first?"},
			[]string{"Here is my question:", "what did you change first?"}},
		{"a break before four words doesn't end the first phrase",
			[]string{"Sure, tell me about the rebuild."},
			[]string{"Sure, tell me about the rebuild."}},
		{"an opening word before a colon stays with what follows",
			[]string{"So: what did you change first?"},
			[]string{"So: what did you change first?"}},
		{"a first sentence under four words is the first phrase",
			[]string{"Got it. What did you change first?"},
			[]string{"Got it.", "What did you change first?"}},
		{"a one-word first sentence is the first phrase",
			[]string{"Okay. ", "Tell me more about the rebuild."},
			[]string{"Okay.", "Tell me more about the rebuild."}},
		{"a short first question is the first phrase",
			[]string{"Really? Why did the team pick that?"},
			[]string{"Really?", "Why did the team pick that?"}},
		{"after a short first sentence the next phrase ends at a break too",
			[]string{"Okay. Where did that version come from, and who assigned it? Tell me, briefly."},
			[]string{"Okay.", "Where did that version come from,", "and who assigned it?", "Tell me, briefly."}},
		{"a reply of one short sentence is one phrase",
			[]string{"Okay."},
			[]string{"Okay."}},
		{"short sentences in a row are each a phrase",
			[]string{"Okay. Sure. Go on, then."},
			[]string{"Okay.", "Sure.", "Go on, then."}},
		{"a title's period ends no sentence",
			[]string{"Hi Dr. Lee, thanks for joining. Good morning, Ms. Sato is here too."},
			[]string{"Hi Dr. Lee, thanks for joining.", "Good morning, Ms. Sato is here too."}},
		{"a word with periods inside ends no sentence",
			[]string{"Okay. U.S. teams, how many did you have? E.g. the pricing team."},
			[]string{"Okay.", "U.S. teams, how many did you have?", "E.g. the pricing team."}},
		{"full stops as an ellipsis end a sentence",
			[]string{"Well... tell me more."},
			[]string{"Well...", "tell me more."}},
		{"a short sentence holds a letter",
			[]string{"… Okay. 2. Tell me more."},
			[]string{"… Okay.", "2. Tell me more."}},
		{"the first phrase ends at a dash",
			[]string{"That sounds like a hard call—what did you do?"},
			[]string{"That sounds like a hard call—", "what did you do?"}},
		{"the first phrase ends at a spaced hyphen, never inside a word",
			[]string{"It was a well-known problem - for one team."},
			[]string{"It was a well-known problem -", "for one team."}},
		{"later phrases are whole sentences, never cut at a clause",
			[]string{"Thanks for sharing that, now tell me more. What did you build, and why did it matter?"},
			[]string{"Thanks for sharing that,", "now tell me more.", "What did you build, and why did it matter?"}},
		{"a comma split across pieces counts once it is whole",
			[]string{"We saved about 1,", "200 dollars a month."},
			[]string{"We saved about 1,200 dollars a month."}},
		{"a dash between digits stays inside the word",
			[]string{"I worked there from 2020–2023, mostly on pricing."},
			[]string{"I worked there from 2020–2023,", "mostly on pricing."}},
	})
}

func TestWithJoiningWordsOnTheFirstPhraseAlsoEndsBeforeOne(t *testing.T) {
	checkCuts(t, true, []cutCase{
		{"the first phrase ends before a joining word",
			[]string{"Tell me about a time when you disagreed with your manager."},
			[]string{"Tell me about a time", "when you disagreed with your manager."}},
		{"a joining word counts only once it is whole",
			[]string{"Tell me about it when", "ever you like."},
			[]string{"Tell me about it whenever you like."}},
		{"a break the reply ends on still cuts",
			[]string{"Tell me about the time when"},
			[]string{"Tell me about the time", "when"}},
		{"words that are often not joining words don't cut",
			[]string{"Can you walk me through that decision? What did you learn after that? Tell me about your team and your manager."},
			[]string{"Can you walk me through that decision?", "What did you learn after that?", "Tell me about your team and your manager."}},
		{"after a short first sentence the next phrase also ends before a joining word",
			[]string{"Okay. Tell me about a time when you disagreed."},
			[]string{"Okay.", "Tell me about a time", "when you disagreed."}},
		{"a joining word bound to the word before doesn't cut",
			[]string{"I would keep going even if it failed."},
			[]string{"I would keep going even if it failed."}},
	})
}

func checkCuts(t *testing.T, joiningWords bool, cases []cutCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, p := range cutAll(joiningWords, c.pieces...) {
				got = append(got, p.text)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("phrases %q, want %q", got, c.want)
			}
		})
	}
}

// cutAll cuts a whole reply streamed as pieces, cutting before joining words when joiningWords
// is set.
func cutAll(joiningWords bool, pieces ...string) []cutPhrase {
	p := phrases{joiningWords: joiningWords}
	var all []cutPhrase
	for _, piece := range pieces {
		all = append(all, p.add(piece)...)
	}
	return append(all, p.end()...)
}

func TestEachPhraseMarksWhereItEndsInTheReply(t *testing.T) {
	reply := " Thanks for sharing that, now tell me more. Okay. Go on.\n"
	got := cutAll(false, reply[:20], reply[20:])
	want := []cutPhrase{{"Thanks for sharing that,", 25}, {"now tell me more.", 43}, {"Okay. Go on.", 56}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("phrases %+v, want %+v", got, want)
	}
	for _, p := range got {
		if !strings.HasSuffix(reply[:p.end], p.text) {
			t.Errorf("the reply up to %d is %q, want it to end with %q", p.end, reply[:p.end], p.text)
		}
	}
}

func TestTheLastPhraseWithoutPunctuationEndsAtItsLastWord(t *testing.T) {
	reply := "Walk me through your work. Then tell me \n"
	got := cutAll(false, reply[:30], reply[30:])
	want := []cutPhrase{{"Walk me through your work.", 26}, {"Then tell me", 39}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("phrases %+v, want %+v: the last ends after its last word, not the spaces after it", got, want)
	}
}

func TestAShortSentenceAtTheEndOfAPieceIsCutAtOnce(t *testing.T) {
	// The model's text arrives in bursts; one ending at "Okay." shouldn't wait for the next.
	for _, tc := range []struct {
		name, piece string
		want        []cutPhrase
	}{
		{"a sentence end after a word", "Okay.", []cutPhrase{{"Okay.", 5}}},
		{"a question", "Really?", []cutPhrase{{"Really?", 7}}},
		{"a period after a number may go on", "We spent 2.", nil},
		{"a period after one letter may go on", "Ask the e.", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p phrases
			if got := p.add(tc.piece); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("phrases %+v, want %+v", got, tc.want)
			}
		})
	}
}
