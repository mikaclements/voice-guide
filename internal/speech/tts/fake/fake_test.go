package fake_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/mikaclements/voice-guide/internal/speech"
	"github.com/mikaclements/voice-guide/internal/speech/tts/fake"
)

// streamed returns a channel that yields pieces, then closes.
func streamed(pieces ...string) <-chan string {
	text := make(chan string, len(pieces))
	for _, p := range pieces {
		text <- p
	}
	close(text)
	return text
}

// untimed returns speech without the times its phrases were ready, checking each has one.
func untimed(t *testing.T, out <-chan speech.Packet) []speech.Packet {
	t.Helper()
	var all []speech.Packet
	for s := range out {
		if s.Phrase.Ready.IsZero() {
			t.Errorf("phrase %d has no time it was ready", s.Phrase.Index)
		}
		s.Phrase.Ready = time.Time{}
		all = append(all, s)
	}
	return all
}

func TestEachPieceOfTextIsSpokenAsSilenceAtTheSetPacketsPerWord(t *testing.T) {
	tts := fake.TextToSpeech{PacketsPerWord: 2}
	got := untimed(t, tts.Speak(t.Context(), "any voice", 1, streamed("Tell me more ", " ", "about it.")))

	first := speech.Phrase{Index: 0, Text: "Tell me more", End: 12, Packets: 6}
	second := speech.Phrase{Index: 1, Text: "about it.", End: 23, Packets: 4}
	var want []speech.Packet
	for range 6 {
		want = append(want, speech.Packet{Opus: fake.Silence, Phrase: first})
	}
	for range 4 {
		want = append(want, speech.Packet{Opus: fake.Silence, Phrase: second})
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("speech %v, want %v", got, want)
	}
}

func TestTheFakeFailsWhereItIsToldTo(t *testing.T) {
	broke := errors.New("speech broke")
	tts := fake.TextToSpeech{PacketsPerWord: 1, Fail: broke, FailAfter: 2}
	got := untimed(t, tts.Speak(t.Context(), "any voice", 1, streamed("Tell me more.", "About it.")))

	if len(got) != 3 || got[0].Err != nil || got[1].Err != nil || got[2].Err != broke || got[2].Phrase.Index != 0 {
		t.Errorf("speech %+v, want two packets, then the failure", got)
	}
}

func TestTheTextIsReadWhileNobodyTakesTheSpeech(t *testing.T) {
	text := make(chan string)
	fake.TextToSpeech{}.Speak(t.Context(), "any voice", 1, text)
	for range 10 {
		select {
		case text <- "Tell me more about the rebuild.":
		case <-time.After(time.Second):
			t.Fatal("the text was held back until its speech was taken")
		}
	}
	close(text)
}

func TestAFailureKeepsTheRepliesTextFlowing(t *testing.T) {
	text := make(chan string)
	out := fake.TextToSpeech{Fail: errors.New("speech broke")}.Speak(t.Context(), "any voice", 1, text)
	text <- "Tell me more."
	if s := <-out; s.Err == nil {
		t.Fatalf("speech %+v, want the failure", s)
	}
	for range 10 {
		select {
		case text <- "Tell me more about the rebuild.":
		case <-time.After(time.Second):
			t.Fatal("the text was held back after the speech failed")
		}
	}
	close(text)
}

func TestCancellingStopsTheSpeech(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	out := fake.TextToSpeech{}.Speak(ctx, "any voice", 1, streamed("Tell me more about the rebuild."))
	<-out
	cancel()
	after := 0
	for range out {
		after++
	}
	if after > 1 { // one may have been on its way as the speech was stopped
		t.Errorf("%d packets after stopping, want at most 1", after)
	}
}
