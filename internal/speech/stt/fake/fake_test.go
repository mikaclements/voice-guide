package fake_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/mikaclements/voice-guide/internal/speech"
	"github.com/mikaclements/voice-guide/internal/speech/stt/fake"
)

func TestEachUtteranceHearsTheScriptThenItsFinalTextOrTheFailure(t *testing.T) {
	broken := errors.New("broken")
	stt := fake.SpeechToText{Rough: []string{"so", "so the"}, Final: "so the plan", Fail: broken, FailUtterances: []int{1}}
	piece := speech.UtteranceAudio{Samples: make([]int16, 320)}
	ended := speech.UtteranceAudio{Samples: make([]int16, 320), End: true}
	audio := make(chan speech.UtteranceAudio)
	heard := stt.Transcribe(context.Background(), audio)
	go func() {
		defer close(audio)
		for _, a := range []speech.UtteranceAudio{piece, piece, ended, ended, {Drop: true}, {End: true}} {
			audio <- a
		}
	}()
	var got []speech.UtteranceText
	for h := range heard {
		got = append(got, h)
	}
	want := []speech.UtteranceText{
		{Utterance: 0, Text: "so"}, {Utterance: 0, Text: "so the"}, {Utterance: 0, Text: "so the plan", Final: true},
		{Utterance: 1, Text: "so"}, {Utterance: 1, Err: broken},
		{Utterance: 3, Final: true},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("heard %+v, want %+v", got, want)
	}
}

func TestCancellingEndsTheStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	heard := fake.SpeechToText{Final: "words"}.Transcribe(ctx, make(chan speech.UtteranceAudio))
	cancel()
	for h := range heard {
		t.Fatalf("heard %+v after cancelling", h)
	}
}
