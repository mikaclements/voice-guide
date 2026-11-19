// Package fake hears a script instead of the audio: speech's fake speech-to-text, for the core's
// tests and for load tests.
package fake

import (
	"context"
	"slices"

	"github.com/mikaclements/voice-guide/internal/speech"
)

// SpeechToText hears Rough[i] as an utterance's rough text after its i-th piece of audio, until
// the script runs out, and Final once it ends; an utterance ended with no audio has an empty final
// text. With Fail set, every utterance numbered in FailUtterances fails with it when it ends.
type SpeechToText struct {
	Rough          []string
	Final          string
	Fail           error
	FailUtterances []int
}

var _ speech.SpeechToText = SpeechToText{}

func (f SpeechToText) Transcribe(ctx context.Context, audio <-chan speech.UtteranceAudio) <-chan speech.UtteranceText {
	out := make(chan speech.UtteranceText)
	go func() {
		defer close(out)
		n, pieces := 0, 0
		for {
			var a speech.UtteranceAudio
			select {
			case got, ok := <-audio:
				if !ok {
					return
				}
				a = got
			case <-ctx.Done():
				return
			}
			heard := []speech.UtteranceText{}
			if len(a.Samples) > 0 && pieces < len(f.Rough) {
				heard = append(heard, speech.UtteranceText{Utterance: n, Text: f.Rough[pieces]})
			}
			if len(a.Samples) > 0 {
				pieces++
			}
			if a.End {
				final := speech.UtteranceText{Utterance: n, Final: true}
				if pieces > 0 {
					final.Text = f.Final
				}
				if f.Fail != nil && slices.Contains(f.FailUtterances, n) {
					final = speech.UtteranceText{Utterance: n, Err: f.Fail}
				}
				heard = append(heard, final)
			}
			if a.End || a.Drop {
				n, pieces = n+1, 0
			}
			for _, t := range heard {
				select {
				case out <- t:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}
