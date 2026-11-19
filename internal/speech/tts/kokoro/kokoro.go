// Package kokoro speaks through Kokoro-FastAPI: speech's text-to-speech adapter. It cuts each
// reply into phrases, asks Kokoro for each phrase's speech as Ogg Opus, and unwraps the 20 ms
// packets that go out on the audio track.
package kokoro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mikaclements/voice-guide/internal/speech"
)

// phrasesAhead bounds the phrases cut but not yet asked for. A reply holds a few sentences, so
// the text is read as it comes; the bound only stops a runaway reply from filling memory.
const phrasesAhead = 64

// maxPhrasePackets bounds the packets held for one phrase while it is read whole: 60s of audio,
// where a phrase is a sentence of a few seconds, so more means a broken stream, and reading on
// would only fill memory. A round guess, far from any sentence measured.
const maxPhrasePackets = 3000

// maxPhraseBytes bounds the bytes held for one phrase: 3,000 packets of the largest Opus frame,
// 1,275 bytes, rounded up, so oversized packets can't fill memory within the packet bound. Derived
// from that bound; Kokoro's packets measured at most 327 bytes.
const maxPhraseBytes = 4 << 20

// DefaultFirstAudio is how long Kokoro has to make a phrase unless set otherwise. It made a
// 16-word sentence in 1.33s on an M3 Pro, so 5s leaves room for longer sentences or a busy
// Kokoro, while a user left in silence learns soon that something failed.
const DefaultFirstAudio = 5 * time.Second

// Options tune how Kokoro is asked; the zero value is the default.
type Options struct {
	// FirstAudio is how long a phrase may take to start arriving before it fails, and then
	// to finish arriving; DefaultFirstAudio when zero or less.
	FirstAudio time.Duration
	// JoiningWords also ends an early phrase before "when", "because" or "if". Off by default:
	// grammar comes first, and the speed it buys (first audio 0.76s cut at "when", against about
	// 1.5s for a 15-word sentence whole) is to be measured on real replies before it is used.
	JoiningWords bool
}

// TextToSpeech asks Kokoro-FastAPI for speech.
type TextToSpeech struct {
	url    string
	o      Options
	client *http.Client
}

var _ speech.TextToSpeech = (*TextToSpeech)(nil)

// New returns text-to-speech that asks Kokoro-FastAPI at baseURL, such as
// http://127.0.0.1:8880.
func New(baseURL string, o Options) *TextToSpeech {
	if o.FirstAudio <= 0 {
		// A deadline already past would fail every phrase before Kokoro could answer.
		o.FirstAudio = DefaultFirstAudio
	}
	return &TextToSpeech{url: strings.TrimSuffix(baseURL, "/") + "/v1/audio/speech", o: o, client: &http.Client{}}
}

// Speak asks Kokoro for each phrase once the phrase before is made, so the next is made while
// this one plays, one at a time: asked for together, phrases slow each other (measured on an M3
// Pro: a 6-word phrase took 0.77s alone, 2.0s beside the next). Never more than one phrase is
// made ahead of what plays, so stopping wastes little. Kokoro sends a phrase's audio only once
// it has made all of it, which is why the first phrase is short. Cancelling ctx closes Kokoro's
// request too. After a failure it keeps reading text until text is closed.
func (k *TextToSpeech) Speak(ctx context.Context, voice string, text <-chan string) <-chan speech.Packet {
	out := make(chan speech.Packet)
	go func() {
		// Last: after a failure the text is still read to its end, so the reply reaches the page.
		defer drain(ctx, text)
		defer close(out)
		ctx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		defer wg.Wait()
		defer cancel()

		phrases := make(chan speech.Phrase, phrasesAhead)
		wg.Go(func() {
			defer close(phrases)
			cut(ctx, text, phrases, k.o.JoiningWords)
		})
		// Each phrase's speech streams on its own channel.
		queue := make(chan chan speech.Packet)
		wg.Go(func() {
			defer close(queue)
			for index := 0; ; index++ {
				var phrase speech.Phrase
				select {
				case next, ok := <-phrases:
					if !ok {
						return
					}
					phrase = next
				case <-ctx.Done():
					return
				}
				phrase.Index = index
				audio, made := make(chan speech.Packet), make(chan struct{})
				wg.Go(func() {
					defer close(audio)
					k.speak(ctx, voice, phrase, audio, made)
				})
				select {
				case queue <- audio:
				case <-ctx.Done():
					return
				}
				select {
				case <-made:
				case <-ctx.Done():
					return
				}
			}
		})
		for audio := range queue {
			for s := range audio {
				if !send(ctx, out, s) || s.Err != nil {
					return
				}
			}
		}
	}()
	return out
}

// cut reads the reply's text until it is closed and sends each phrase once it is complete.
func cut(ctx context.Context, text <-chan string, out chan<- speech.Phrase, joiningWords bool) {
	p := phrases{joiningWords: joiningWords}
	for {
		var done []cutPhrase
		select {
		case t, ok := <-text:
			if ok {
				done = p.add(t)
			} else {
				done = p.end()
			}
			for _, d := range done {
				select {
				case out <- speech.Phrase{Text: d.text, End: d.end, Ready: time.Now()}:
				case <-ctx.Done():
					return
				}
			}
			if !ok {
				return
			}
		case <-ctx.Done():
			return
		}
	}
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

// speak asks Kokoro for one phrase and sends its packets once all have arrived, or those that
// came and the error that stopped them. It closes made once the first packet arrives, when Kokoro has made the
// phrase, or when it gives up, and gives up if that takes longer than firstAudio.
func (k *TextToSpeech) speak(ctx context.Context, voice string, phrase speech.Phrase, audio chan<- speech.Packet, made chan<- struct{}) {
	asking, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	slow := fmt.Errorf("no audio within %v", k.o.FirstAudio)
	stalled := fmt.Errorf("the rest of the phrase not within %v of its first packet", k.o.FirstAudio)
	timer := time.AfterFunc(k.o.FirstAudio, func() { stop(slow) })
	var once sync.Once
	var rest *time.Timer
	first := func() {
		once.Do(func() {
			timer.Stop()
			// Kokoro sends a phrase all at once, so the rest follows the first packet at once; one
			// that stalls fails, rather than holding the speech or going quietly missing.
			rest = time.AfterFunc(k.o.FirstAudio, func() { stop(stalled) })
			close(made)
		})
	}
	defer func() {
		once.Do(func() { // no packet came
			timer.Stop()
			close(made)
		})
		if rest != nil {
			rest.Stop()
		}
	}()
	// Kokoro sends a phrase only once all of it is made, so reading it whole before the first
	// packet goes costs only the reading, and says how many packets the phrase has.
	var packets [][]byte
	var size int
	var tooBig error
	err := k.ask(asking, voice, phrase.Text, func(packet []byte) bool {
		first()
		size += len(packet)
		switch {
		case len(packets) == maxPhrasePackets:
			tooBig = fmt.Errorf("over %d packets", maxPhrasePackets)
		case size > maxPhraseBytes:
			tooBig = fmt.Errorf("over %d bytes", maxPhraseBytes)
		default:
			packets = append(packets, packet)
			return true
		}
		packets = nil // a broken stream, not speech: none of it is played
		return false
	})
	switch context.Cause(asking) {
	case slow:
		err = slow
	case stalled:
		err = stalled
	}
	if err == nil && tooBig != nil {
		err = tooBig
	}
	if err == nil {
		phrase.Packets = len(packets)
	}
	for _, packet := range packets {
		if !send(ctx, audio, speech.Packet{Opus: packet, Phrase: phrase}) {
			return
		}
	}
	if err != nil {
		send(ctx, audio, speech.Packet{Phrase: phrase, Err: fmt.Errorf("kokoro: phrase %d: %w", phrase.Index, err)})
	}
}

func (k *TextToSpeech) ask(ctx context.Context, voice, text string, packet func([]byte) bool) error {
	body, err := json.Marshal(map[string]any{
		"model": "kokoro", "input": text, "voice": voice, "response_format": "opus", "stream": true,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	// The status code only, in words of our own: Kokoro's reasons, its status line's included,
	// can quote the text it was given, which holds what the user said, and errors reach logs.
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("refused with status %d", resp.StatusCode)
	}
	return readOpus(resp.Body, packet)
}

// send hands s on unless ctx has stopped the speech: a caller draining stopped speech gets
// nothing more.
func send(ctx context.Context, out chan<- speech.Packet, s speech.Packet) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case out <- s:
		return true
	case <-ctx.Done():
		return false
	}
}
