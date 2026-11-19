// Package realtime transcribes through a service speaking OpenAI's realtime transcription API
// over a WebSocket: speech's speech-to-text adapter. One connection, opened at a stream's first
// audio, carries every utterance as an item: its audio appended, resampled to 24 kHz, the only
// rate the API takes, then committed, and its texts matched to it by the item's ID, since the
// API doesn't promise final texts in commit order.
package realtime

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/mikaclements/voice-guide/internal/speech"
)

const (
	// DefaultConnect bounds opening the connection and its session unless set otherwise. The
	// local service answers in milliseconds and a cloud one in a few hundred; 2 s, a round guess
	// to measure when a cloud service is tried, keeps a user from waiting long on one not there.
	DefaultConnect = 2 * time.Second
	// writeDeadline bounds sending one event, which only waits when the service has stopped
	// reading; a second, a round guess, means it is stuck.
	writeDeadline = time.Second
	// DefaultFinalText bounds the wait for an utterance's final text once it has ended, unless set
	// otherwise. It came 116 to 258 ms after the audio's end over every
	// .claude/skills/measure/check-stt.sh run on an M3 Pro, so 2 s, about eight times the slowest, is reached only by a stuck service;
	// measured again with spoken turns.
	DefaultFinalText = 2 * time.Second
	// textsAhead bounds the texts waiting to be taken: several seconds of speech, as rough text
	// grows about twice a second; the bound only stops a stalled reader from filling memory.
	textsAhead = 16
)

// sessionUpdate asks for transcription without turn detection, so the end of each utterance is
// speech's to decide. It names no model: the service is set up with the one it runs.
const sessionUpdate = `{"type":"session.update","session":{"type":"transcription",` +
	`"audio":{"input":{"format":{"type":"audio/pcm","rate":24000},"turn_detection":null}}}}`

// Options bound the adapter's waits; zero means the default.
type Options struct {
	Connect, FinalText time.Duration
}

// SpeechToText transcribes through the service at its address.
type SpeechToText struct {
	url string
	o   Options
}

var _ speech.SpeechToText = (*SpeechToText)(nil)

// New returns speech-to-text through the service at baseURL, a ws or wss address.
func New(baseURL string, o Options) (*SpeechToText, error) {
	if u, err := url.Parse(baseURL); err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		return nil, fmt.Errorf("%q is not a ws or wss address", baseURL)
	}
	o.Connect, o.FinalText = cmp.Or(o.Connect, DefaultConnect), cmp.Or(o.FinalText, DefaultFinalText)
	return &SpeechToText{url: strings.TrimSuffix(baseURL, "/") + "/v1/realtime?intent=transcription", o: o}, nil
}

func (s *SpeechToText) Transcribe(ctx context.Context, audio <-chan speech.UtteranceAudio) <-chan speech.UtteranceText {
	out := make(chan speech.UtteranceText, textsAhead)
	go func() {
		defer close(out)
		st := &stream{SpeechToText: s, out: out, settled: map[int]bool{}, items: map[string]int{}, rough: map[int]string{}, due: map[int]time.Time{}}
		defer st.disconnect()
		st.run(ctx, audio)
	}()
	return out
}

// event is what the service sends, as far as the adapter reads it, or the end of its connection.
type event struct {
	Type       string `json:"type"`
	ItemID     string `json:"item_id"`
	Delta      string `json:"delta"`
	Transcript string `json:"transcript"`
	Error      struct {
		Type string `json:"type"`
		Code string `json:"code"`
	} `json:"error"`
	err error
}

// stream is one audio connection's speech-to-text, run by one goroutine.
type stream struct {
	*SpeechToText
	out chan<- speech.UtteranceText
	// conn is the open connection, if any; events carries what its reader read, and stop ends
	// the reader and waits for it.
	conn   *websocket.Conn
	events chan event
	stop   func()
	// open is the utterance being appended; sent says some of its audio went out, lost that it
	// failed, so the rest of it is passed over and its end clears what the service holds of it.
	open       int
	sent, lost bool
	up         upsampler
	// waiting are utterances committed or cleared with no item ID yet, oldest first, so an item
	// first seen goes to the oldest of them; one already answered, or cleared, stays in settled
	// until its item comes, a refused commit or the clear's confirmation, so its item is never
	// taken for a later one's. clears are the cleared ones awaiting confirmation. items maps
	// live item IDs to utterances, and gone keeps the last items whose events no longer count.
	// due is when each committed utterance's final text is late.
	waiting []int
	settled map[int]bool
	clears  []int
	items   map[string]int
	gone    []string
	rough   map[int]string
	due     map[int]time.Time
}

// goneKept is how many forgotten items are remembered. Their late events follow within a
// recognition pass or two, so a handful covers them; the bound keeps a long session from
// growing the list.
const goneKept = 16

func (st *stream) run(ctx context.Context, audio <-chan speech.UtteranceAudio) {
	late := time.NewTimer(time.Hour)
	defer late.Stop()
	for audio != nil || len(st.due) > 0 {
		st.resetLate(late)
		select {
		case a, ok := <-audio:
			if !ok {
				audio = nil
				continue
			}
			st.take(ctx, a)
		case e := <-st.events:
			st.handle(ctx, e)
		case <-late.C:
			// A final text this late means a stuck service: the connection goes, and with it
			// every utterance it carried.
			st.broken(ctx, fmt.Errorf("no final text within %s", st.o.FinalText))
		case <-ctx.Done():
			return
		}
	}
}

func (st *stream) resetLate(late *time.Timer) {
	next := time.Now().Add(time.Hour)
	for _, at := range st.due {
		if at.Before(next) {
			next = at
		}
	}
	late.Reset(max(0, time.Until(next)))
}

func (st *stream) take(ctx context.Context, a speech.UtteranceAudio) {
	n := st.open
	if len(a.Samples) > 0 && !st.lost {
		st.sent = true
		st.write(ctx, "utterance-"+strconv.Itoa(n), map[string]string{"type": "input_audio_buffer.append", "audio": pcm(st.up.up(a.Samples))})
	}
	if !a.End && !a.Drop {
		return
	}
	switch {
	case a.End && !st.sent:
		// Nothing was sent, as with a sound taken for speech at first, so nothing is asked for.
		st.deliver(ctx, speech.UtteranceText{Utterance: n, Final: true})
	case a.End && !st.lost:
		if st.write(ctx, "commit-"+strconv.Itoa(n), map[string]string{"type": "input_audio_buffer.commit"}) {
			if !slices.Contains(slices.Collect(maps.Values(st.items)), n) {
				st.waiting = append(st.waiting, n)
			}
			st.due[n] = time.Now().Add(st.o.FinalText)
		}
	case st.sent && st.conn != nil:
		// Dropped, or lost on the way: what the service holds of it goes, so the next utterance
		// starts clean.
		st.forgetItems(n)
		delete(st.rough, n)
		if st.write(ctx, "utterance-"+strconv.Itoa(n), map[string]string{"type": "input_audio_buffer.clear"}) {
			st.waiting, st.clears, st.settled[n] = append(st.waiting, n), append(st.clears, n), true
		}
	}
	st.open, st.sent, st.lost, st.up = n+1, false, false, upsampler{}
}

// write sends one event with its event ID, opening the connection first if there is none; a
// failure breaks the connection.
func (st *stream) write(ctx context.Context, id string, e map[string]string) bool {
	if st.conn == nil {
		if err := st.connect(ctx); err != nil {
			st.broken(ctx, err)
			return false
		}
	}
	e["event_id"] = id
	wctx, cancel := context.WithTimeout(ctx, writeDeadline)
	defer cancel()
	if err := wsjson.Write(wctx, st.conn, e); err != nil {
		st.broken(ctx, fmt.Errorf("send to speech-to-text: %w", err))
		return false
	}
	return true
}

func (st *stream) connect(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, st.o.Connect)
	defer cancel()
	conn, _, err := websocket.Dial(cctx, st.url, nil)
	if err != nil {
		return fmt.Errorf("connect to speech-to-text: %w", err)
	}
	if err := conn.Write(cctx, websocket.MessageText, []byte(sessionUpdate)); err != nil {
		_ = conn.CloseNow()
		return fmt.Errorf("start speech-to-text: %w", err)
	}
	rctx, stop := context.WithCancel(ctx)
	events := make(chan event)
	var wg sync.WaitGroup
	wg.Go(func() { read(rctx, conn, events) })
	st.conn, st.events = conn, events
	st.stop = func() { stop(); _ = conn.CloseNow(); wg.Wait() }
	return nil
}

func (st *stream) disconnect() {
	if st.conn != nil {
		st.stop()
		st.conn, st.events = nil, nil
	}
}

// broken drops the connection, failing every utterance it carried that has no answer yet; the
// next audio opens another.
func (st *stream) broken(ctx context.Context, err error) {
	st.disconnect()
	failed := slices.Concat(st.waiting, slices.Collect(maps.Values(st.items)))
	if st.sent && !st.lost {
		failed = append(failed, st.open)
	}
	slices.Sort(failed)
	for _, n := range slices.Compact(failed) {
		if !st.settled[n] {
			st.fail(ctx, n, err)
		}
	}
	st.waiting, st.settled, st.clears, st.items, st.gone = nil, map[int]bool{}, nil, map[string]int{}, nil
}

// fail answers utterance n with err; its item's later events don't count.
func (st *stream) fail(ctx context.Context, n int, err error) {
	if n == st.open {
		st.lost = true
	}
	if slices.Contains(st.waiting, n) {
		st.settled[n] = true
	}
	st.forgetItems(n)
	delete(st.due, n)
	delete(st.rough, n)
	st.deliver(ctx, speech.UtteranceText{Utterance: n, Err: err})
}

func (st *stream) forgetItems(n int) {
	for item, m := range st.items {
		if m == n {
			delete(st.items, item)
			st.forget(item)
		}
	}
}

func (st *stream) forget(item string) {
	st.gone = append(st.gone, item)
	if len(st.gone) > goneKept {
		st.gone = st.gone[1:]
	}
}

func (st *stream) handle(ctx context.Context, e event) {
	if e.err != nil {
		st.broken(ctx, e.err)
		return
	}
	// The type and code only: a message can quote what was said, and errors reach logs.
	failure := fmt.Errorf("speech-to-text failed: %s", strings.TrimSpace(e.Error.Type+" "+e.Error.Code))
	switch e.Type {
	case "input_audio_buffer.cleared":
		if len(st.clears) > 0 {
			n := st.clears[0]
			st.clears = st.clears[1:]
			st.waiting = slices.DeleteFunc(st.waiting, func(m int) bool { return m == n })
			delete(st.settled, n)
		}
	case "error", "conversation.item.input_audio_transcription.failed":
		// Whatever the service failed, the connection goes with it: every utterance it carried
		// gets its one failure, its later events go unread, and the next speech opens another.
		st.broken(ctx, failure)
	case "input_audio_buffer.committed", "conversation.item.input_audio_transcription.delta",
		"conversation.item.input_audio_transcription.completed":
		n, ok := st.utterance(e.ItemID)
		if !ok {
			return
		}
		switch e.Type {
		case "conversation.item.input_audio_transcription.delta":
			st.rough[n] += e.Delta
			st.deliver(ctx, speech.UtteranceText{Utterance: n, Text: st.rough[n]})
		case "conversation.item.input_audio_transcription.completed":
			delete(st.items, e.ItemID)
			st.forget(e.ItemID)
			delete(st.due, n)
			delete(st.rough, n)
			st.deliver(ctx, speech.UtteranceText{Utterance: n, Text: e.Transcript, Final: true})
		}
	}
}

// utterance is the live utterance an item belongs to. An item first seen belongs to the oldest
// committed or cleared utterance without one, or else to the open one; one that belongs to an
// answered, cleared or lost utterance doesn't count.
func (st *stream) utterance(item string) (int, bool) {
	if n, ok := st.items[item]; ok {
		return n, true
	}
	if slices.Contains(st.gone, item) {
		return 0, false
	}
	switch {
	case len(st.waiting) > 0:
		n := st.waiting[0]
		st.waiting = st.waiting[1:]
		if st.settled[n] {
			delete(st.settled, n)
			st.forget(item)
			return 0, false
		}
		st.items[item] = n
		return n, true
	case st.lost:
		st.forget(item)
		return 0, false
	}
	st.items[item] = st.open
	return st.open, true
}

func (st *stream) deliver(ctx context.Context, t speech.UtteranceText) {
	select {
	case st.out <- t:
	case <-ctx.Done():
	}
}

// read passes on what the service sends until the connection ends. A close keeps only its code:
// its reason is the service's to word and could quote what was said.
func read(ctx context.Context, conn *websocket.Conn, events chan<- event) {
	for {
		var e event
		err := wsjson.Read(ctx, conn, &e)
		if code := websocket.CloseStatus(err); code != -1 {
			err = fmt.Errorf("speech-to-text closed the connection with code %d", code)
		} else if err != nil {
			err = fmt.Errorf("read from speech-to-text: %w", errors.Unwrap(err))
		}
		e.err = err
		select {
		case events <- e:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

// pcm is the API's audio: 16-bit little-endian samples in base64.
func pcm(samples []int16) string {
	b := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	return base64.StdEncoding.EncodeToString(b)
}
