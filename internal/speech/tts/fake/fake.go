// Package fake speaks every reply as silence at the pace of speech: speech's fake
// text-to-speech, for the core's tests and for load tests.
package fake

import (
	"cmp"
	"context"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/mikaclements/voice-guide/internal/speech"
)

// Silence is an Opus packet holding 20 ms of silence (fullband, mono, one frame), which any
// Opus decoder plays, so the fake's speech can go out on a real audio track.
var Silence = []byte{0xf8, 0xff, 0xfe}

// wordPackets is each word's speech unless PacketsPerWord is set: 20 packets, 0.4s, about 150
// words a minute, an everyday speaking pace, so a fake reply lasts as long as a spoken one.
const wordPackets = 20

// piecesAhead bounds the text read but not yet spoken. The model streams a reply a few words at
// a time, so a long reply is a few hundred pieces, and the text is read as it comes.
const piecesAhead = 1024

// TextToSpeech speaks each piece of text it reads as one phrase, in any voice: PacketsPerWord
// packets of silence per word, sent as fast as they are taken. With Fail set, the speech fails
// with it after FailAfter packets.
type TextToSpeech struct {
	PacketsPerWord int
	Fail           error
	FailAfter      int
}

var _ speech.TextToSpeech = TextToSpeech{}

// Speak reads the text on its own goroutine, so speaking never holds the text back, and keeps
// reading it after a failure until it is closed.
func (f TextToSpeech) Speak(ctx context.Context, _ string, _ float64, text <-chan string) <-chan speech.Packet {
	out := make(chan speech.Packet)
	go func() {
		defer drain(ctx, text)
		defer close(out)
		ctx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		defer wg.Wait()
		defer cancel()
		phrases := make(chan speech.Phrase, piecesAhead)
		wg.Go(func() {
			defer close(phrases)
			for read := 0; ; {
				select {
				case t, ok := <-text:
					if !ok {
						return
					}
					end := read + len(strings.TrimRightFunc(t, unicode.IsSpace))
					read += len(t)
					// Trimmed like a real adapter's phrases, so what was heard is reckoned from the same text.
					spoken := strings.TrimSpace(t)
					if spoken == "" {
						continue
					}
					select {
					case phrases <- speech.Phrase{Text: spoken, End: end, Ready: time.Now()}:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		})
		perWord, index, sent := cmp.Or(f.PacketsPerWord, wordPackets), 0, 0
		for phrase := range phrases {
			phrase.Index = index
			phrase.Packets = perWord * len(strings.Fields(phrase.Text))
			index++
			for range phrase.Packets {
				s := speech.Packet{Opus: Silence, Phrase: phrase}
				if f.Fail != nil && sent == f.FailAfter {
					s = speech.Packet{Phrase: phrase, Err: f.Fail}
				}
				if ctx.Err() != nil { // a caller draining stopped speech gets nothing more
					return
				}
				select {
				case out <- s:
					sent++
				case <-ctx.Done():
					return
				}
				if s.Err != nil {
					return
				}
			}
		}
	}()
	return out
}

// drain reads text until it is closed or ctx ends.
func drain(ctx context.Context, text <-chan string) {
	for {
		select {
		case _, ok := <-text:
			if !ok {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
