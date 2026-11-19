package realtime_test

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/mikaclements/voice-guide/internal/speech"
	"github.com/mikaclements/voice-guide/internal/speech/stt/realtime"
)

// recorded answers like the speech-to-text service did in one real exchange, Kokoro saying "Tell
// me about the last project you shipped." at the pace it was spoken: each recorded event is sent
// after the first client event of the type it followed.
func recorded(t *testing.T) func(int, map[string]any) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/exchange.jsonl")
	if err != nil {
		t.Fatalf("read the recorded exchange: %v", err)
	}
	var events []struct {
		After string          `json:"after"`
		Event json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal([]byte("["+strings.ReplaceAll(strings.TrimSpace(string(b)), "\n", ",")+"]"), &events); err != nil {
		t.Fatalf("read the recorded exchange: %v", err)
	}
	seen := map[string]bool{}
	return func(_ int, sent map[string]any) []string {
		after := "connect"
		if sent != nil {
			after = sent["type"].(string)
		}
		var answer []string
		for _, e := range events {
			if e.After == after && !seen[after] {
				answer = append(answer, string(e.Event))
			}
		}
		seen[after] = true
		return answer
	}
}

// service plays the speech-to-text service: answer gives what to send on connecting (sent nil)
// and after each client event, "close" closing the connection and "hang" going silent. It counts
// connections and keeps what clients sent.
type service struct {
	answer      func(connection int, sent map[string]any) []string
	options     realtime.Options
	mu          sync.Mutex
	connections int
	sent        []map[string]any
	more        chan struct{} // made by a wait, closed when the next client event arrives
}

// types waits up to a second for n client events and returns their types, with the connections.
func (s *service) types(n int) ([]string, int) {
	deadline := time.After(time.Second)
	for {
		s.mu.Lock()
		types := []string{}
		for _, e := range s.sent {
			types = append(types, e["type"].(string))
		}
		c := s.connections
		if s.more == nil {
			s.more = make(chan struct{})
		}
		more := s.more
		s.mu.Unlock()
		if len(types) >= n {
			return types, c
		}
		select {
		case <-more:
		case <-deadline:
			return types, c
		}
	}
}

func (s *service) serve(t *testing.T) *realtime.SpeechToText {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		s.mu.Lock()
		s.connections++
		c := s.connections
		s.mu.Unlock()
		var sent map[string]any
		for {
			for _, e := range s.answer(c, sent) {
				switch e {
				case "close":
					_ = conn.Close(websocket.StatusGoingAway, "what was said")
					return
				case "hang":
					<-r.Context().Done()
					return
				}
				if conn.Write(r.Context(), websocket.MessageText, []byte(e)) != nil {
					return
				}
			}
			_, b, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			sent = nil
			_ = json.Unmarshal(b, &sent)
			s.mu.Lock()
			s.sent = append(s.sent, sent)
			if s.more != nil {
				close(s.more)
				s.more = nil
			}
			s.mu.Unlock()
		}
	}))
	t.Cleanup(srv.Close)
	stt, err := realtime.New("ws"+strings.TrimPrefix(srv.URL, "http"), cmp.Or(s.options, realtime.Options{FinalText: 200 * time.Millisecond}))
	if err != nil {
		t.Fatal(err)
	}
	return stt
}

// utterance is n pieces of 20 ms of audio, every sample 1000, the last ending it.
func utterance(n int) []speech.UtteranceAudio {
	audio := make([]speech.UtteranceAudio, n)
	for i := range audio {
		audio[i].Samples = slices.Repeat([]int16{1000}, 320)
	}
	audio[n-1].End = true
	return audio
}

// transcribe sends the audio over one stream, closes it, and returns everything heard.
func transcribe(t *testing.T, stt *realtime.SpeechToText, audio ...speech.UtteranceAudio) []speech.UtteranceText {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	in := make(chan speech.UtteranceAudio)
	heard := stt.Transcribe(ctx, in)
	go func() {
		defer close(in)
		for _, a := range audio {
			select {
			case in <- a:
			case <-ctx.Done():
				return
			}
		}
	}()
	var got []speech.UtteranceText
	for h := range heard {
		got = append(got, h)
	}
	if ctx.Err() != nil {
		t.Fatalf("the stream didn't end; heard %+v", got)
	}
	return got
}

func TestRoughTextGrowsWhileSpeakingAndTheFinalTextEndsTheUtterance(t *testing.T) {
	s := service{answer: recorded(t)}
	got := transcribe(t, s.serve(t), utterance(10)...)
	want := []speech.UtteranceText{
		{Text: "Tell me about"},
		{Text: "Tell me about the last"},
		{Text: "Tell me about the last project you've"},
		{Text: "Tell me about the last project you shipped.", Final: true},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("heard %+v, want %+v", got, want)
	}
}

func TestTheClientAsksForTranscriptionWithoutTurnDetectionAndCommitsAt24kHz(t *testing.T) {
	s := service{answer: recorded(t)}
	transcribe(t, s.serve(t), utterance(10)...)
	s.mu.Lock()
	defer s.mu.Unlock()
	update, _ := json.Marshal(s.sent[0])
	want := `{"session":{"audio":{"input":{"format":{"rate":24000,"type":"audio/pcm"},"turn_detection":null}},"type":"transcription"},"type":"session.update"}`
	if string(update) != want {
		t.Errorf("first event %s, want %s", update, want)
	}
	appends, bytes := 0, 0
	for _, e := range s.sent[1 : len(s.sent)-1] {
		pcm, err := base64.StdEncoding.DecodeString(e["audio"].(string))
		if e["type"] != "input_audio_buffer.append" || e["event_id"] != "utterance-0" || err != nil {
			t.Fatalf("event %v, want an append of base64 audio for utterance 0", e)
		}
		if appends, bytes = appends+1, bytes+len(pcm); int16(binary.LittleEndian.Uint16(pcm)) != 1000 {
			t.Fatalf("a sample of 1000 arrived as %x, want 16-bit little-endian", pcm[:2])
		}
	}
	// Ten 20 ms frames at 16 kHz are 0.2 s: 4800 samples of 2 bytes at 24 kHz, but the one held.
	if appends != 10 || bytes != 2*4799 {
		t.Errorf("%d appends of %d bytes, want 10 of %d", appends, bytes, 2*4799)
	}
	if last := s.sent[len(s.sent)-1]["type"]; last != "input_audio_buffer.commit" {
		t.Errorf("last event %v, want the commit", last)
	}
}

// committed answers each commit with its item, named after the utterance in the event's ID, and
// the second commit with both final texts, the second utterance's first.
func committed(_ int, sent map[string]any) []string {
	if sent == nil || sent["type"] != "input_audio_buffer.commit" {
		return nil
	}
	item := "item_" + strings.TrimPrefix(sent["event_id"].(string), "commit-")
	events := []string{`{"type":"input_audio_buffer.committed","item_id":"` + item + `"}`}
	if item == "item_1" {
		events = append(events,
			`{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_1","transcript":"second"}`,
			`{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_0","transcript":"first"}`)
	}
	return events
}

func TestFinalTextsArrivingOutOfOrderLandOnTheirUtterancesOverOneConnection(t *testing.T) {
	s := service{answer: committed}
	got := transcribe(t, s.serve(t), slices.Concat(utterance(2), utterance(2))...)
	want := []speech.UtteranceText{{Utterance: 1, Text: "second", Final: true}, {Utterance: 0, Text: "first", Final: true}}
	if _, c := s.types(0); !slices.Equal(got, want) || c != 1 {
		t.Fatalf("heard %+v over %d connections, want %+v over one", got, c, want)
	}
}

func TestAFalseStartAsksTheServiceForNothing(t *testing.T) {
	s := service{answer: committed}
	got := transcribe(t, s.serve(t),
		speech.UtteranceAudio{End: true},
		speech.UtteranceAudio{Samples: make([]int16, 320), Drop: true},
		speech.UtteranceAudio{Drop: true})
	if want := []speech.UtteranceText{{Utterance: 0, Final: true}}; !slices.Equal(got, want) {
		t.Errorf("heard %+v, want only an empty final text for the utterance that ended", got)
	}
	types, _ := s.types(3)
	if want := []string{"session.update", "input_audio_buffer.append", "input_audio_buffer.clear"}; !slices.Equal(types, want) {
		t.Errorf("sent %v, want %v", types, want)
	}
}

func TestAFailureTheServiceNamesEndsItsConnectionAndTheNextUtteranceReconnects(t *testing.T) {
	for name, failure := range map[string]string{
		"an error about a client event": `{"type":"error","error":{"type":"server_error","code":null,"message":"what was said","event_id":"utterance-0"}}`,
		"a failed transcription":        `{"type":"conversation.item.input_audio_transcription.failed","item_id":"item_0","error":{"type":"server_error","message":"what was said"}}`,
	} {
		s := service{answer: script(map[string][]string{
			"input_audio_buffer.commit commit-0": {`{"type":"input_audio_buffer.committed","item_id":"item_0"}`, failure},
			"input_audio_buffer.commit commit-1": {`{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_1","transcript":"next"}`},
		})}
		send, next, rest := talk(t, s.serve(t))
		send(utterance(2)...)
		if h := next(); h.Utterance != 0 || h.Err == nil || strings.Contains(h.Err.Error(), "what was said") || !strings.Contains(h.Err.Error(), "server_error") {
			t.Errorf("%s: heard %+v, want utterance 0 failed naming the error's type and not its message", name, h)
		}
		send(utterance(2)...)
		got := rest()
		if _, c := s.types(0); !slices.Equal(got, []speech.UtteranceText{{Utterance: 1, Text: "next", Final: true}}) || c != 2 {
			t.Errorf("%s: then heard %+v over %d connections, want utterance 1 over a new one", name, got, c)
		}
	}
}

func TestAClosedConnectionFailsWhatItCarriedWithItsCodeAndTheNextUtteranceReconnects(t *testing.T) {
	s := service{answer: func(c int, sent map[string]any) []string {
		if sent == nil || sent["type"] != "input_audio_buffer.commit" {
			return nil
		}
		if c == 1 {
			return []string{"close"}
		}
		return []string{`{"type":"input_audio_buffer.committed","item_id":"item_1"}`,
			`{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_1","transcript":"second"}`}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	in := make(chan speech.UtteranceAudio, 4)
	heard := s.serve(t).Transcribe(ctx, in)
	var got []speech.UtteranceText
	for _, u := range [][]speech.UtteranceAudio{utterance(2), utterance(2)} {
		for _, a := range u {
			in <- a
		}
		got = append(got, <-heard)
	}
	_, c := s.types(0)
	if got[0].Err == nil || !strings.Contains(got[0].Err.Error(), "1001") || strings.Contains(got[0].Err.Error(), "what was said") ||
		got[1] != (speech.UtteranceText{Utterance: 1, Text: "second", Final: true}) || c != 2 {
		t.Fatalf("heard %+v over %d connections, want utterance 0 failed with the close's code only, then utterance 1 over a new one", got, c)
	}
}

func TestAServiceThatGoesSilentFailsTheUtteranceAtTheDeadline(t *testing.T) {
	s := service{answer: func(_ int, sent map[string]any) []string {
		if sent != nil && sent["type"] == "input_audio_buffer.commit" {
			return []string{"hang"}
		}
		return nil
	}}
	started := time.Now()
	got := transcribe(t, s.serve(t), utterance(2)...)
	if len(got) != 1 || got[0].Err == nil || time.Since(started) > time.Second {
		t.Fatalf("heard %+v after %s, want a failure at the 200 ms deadline", got, time.Since(started))
	}
}

func TestAServiceThatCantBeReachedFailsTheUtteranceAndTheNextTriesAgain(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
	stt, _ := realtime.New("ws://"+ln.Addr().String(), realtime.Options{})
	got := transcribe(t, stt, slices.Concat(utterance(2), utterance(2))...)
	if len(got) != 2 || got[0].Utterance != 0 || got[0].Err == nil || got[1].Utterance != 1 || got[1].Err == nil {
		t.Fatalf("heard %+v, want each utterance failed", got)
	}
}

func TestAHandshakeThatNeverCompletesFailsAtTheConnectDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	stt, _ := realtime.New("ws://"+ln.Addr().String(), realtime.Options{Connect: 100 * time.Millisecond})
	started := time.Now()
	if got := transcribe(t, stt, utterance(1)...); len(got) != 1 || got[0].Err == nil || time.Since(started) > time.Second {
		t.Fatalf("heard %+v after %s, want a failure at the 100 ms deadline", got, time.Since(started))
	}
}

func TestCancellingEndsTheStreamPromptly(t *testing.T) {
	s := service{answer: func(int, map[string]any) []string { return []string{"hang"} }}
	stt := s.serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan speech.UtteranceAudio, 1)
	heard := stt.Transcribe(ctx, in)
	in <- utterance(1)[0]
	cancel()
	select {
	case <-func() chan struct{} {
		done := make(chan struct{})
		go func() {
			for range heard {
			}
			close(done)
		}()
		return done
	}():
	case <-time.After(time.Second):
		t.Fatal("the stream was still open a second after cancelling")
	}
}

func TestOnlyWebSocketAddressesAreTaken(t *testing.T) {
	if _, err := realtime.New("http://localhost:8092", realtime.Options{}); err == nil {
		t.Error("New took an http address")
	}
}

// script answers each client event of a type and event ID once, with the events given for it,
// and every clear with the service's confirmation.
func script(answers map[string][]string) func(int, map[string]any) []string {
	return func(_ int, sent map[string]any) []string {
		if sent == nil {
			return nil
		}
		key := fmt.Sprint(sent["type"], " ", sent["event_id"])
		answer := answers[key]
		delete(answers, key)
		if sent["type"] == "input_audio_buffer.clear" {
			answer = append(answer, `{"type":"input_audio_buffer.cleared"}`)
		}
		return answer
	}
}

// talk opens a stream; send queues audio, next takes what is heard next, and rest ends the
// stream and returns whatever else was heard.
func talk(t *testing.T, stt *realtime.SpeechToText) (send func(...speech.UtteranceAudio), next func() speech.UtteranceText, rest func() []speech.UtteranceText) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	in := make(chan speech.UtteranceAudio, 16)
	heard := stt.Transcribe(ctx, in)
	send = func(audio ...speech.UtteranceAudio) {
		for _, a := range audio {
			in <- a
		}
	}
	next = func() speech.UtteranceText {
		h, ok := <-heard
		if !ok || ctx.Err() != nil {
			t.Fatal("nothing more was heard")
		}
		return h
	}
	rest = func() []speech.UtteranceText {
		close(in)
		var got []speech.UtteranceText
		for h := range heard {
			got = append(got, h)
		}
		return got
	}
	return send, next, rest
}

const (
	full       = `{"type":"error","error":{"type":"invalid_request_error","code":"input_audio_buffer_full","event_id":"utterance-0"}}`
	commitNone = `{"type":"error","error":{"type":"invalid_request_error","code":"input_audio_buffer_commit_empty","event_id":"commit-0"}}`
	second     = `{"type":"input_audio_buffer.committed","item_id":"item_1"}|{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_1","transcript":"second"}`
)

func TestAnUtteranceLostPartwayTakesItsConnectionAndTheNextIsUntouched(t *testing.T) {
	s := service{answer: script(map[string][]string{
		"input_audio_buffer.append utterance-0": {full},
		"input_audio_buffer.commit commit-1":    strings.Split(second, "|"),
	})}
	send, next, rest := talk(t, s.serve(t))
	u := utterance(3)
	send(u[0])
	if h := next(); h.Utterance != 0 || h.Err == nil || !strings.Contains(h.Err.Error(), "input_audio_buffer_full") {
		t.Fatalf("heard %+v, want utterance 0 lost to the full buffer", h)
	}
	send(u[1:]...)
	send(utterance(2)...)
	if got, want := rest(), []speech.UtteranceText{{Utterance: 1, Text: "second", Final: true}}; !slices.Equal(got, want) {
		t.Fatalf("then heard %+v, want %+v", got, want)
	}
	if _, c := s.types(0); c != 2 {
		t.Errorf("used %d connections, want the lost utterance's replaced", c)
	}
}

func TestEachUtteranceIsAnsweredOnceWhenAFailureComesAfterItsCommit(t *testing.T) {
	late := `{"type":"input_audio_buffer.committed","item_id":"item_0"}|{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_0","transcript":"late"}`
	for name, afterCommit := range map[string][]string{
		"the service keeps the item":     append([]string{full}, strings.Split(late, "|")...),
		"the service refuses the commit": {full, commitNone},
	} {
		s := service{answer: script(map[string][]string{
			"input_audio_buffer.commit commit-0": afterCommit,
			"input_audio_buffer.commit commit-1": strings.Split(second, "|"),
		})}
		send, next, rest := talk(t, s.serve(t))
		send(utterance(2)...)
		if h := next(); h.Utterance != 0 || h.Err == nil {
			t.Fatalf("%s: heard %+v, want utterance 0 failed", name, h)
		}
		send(utterance(2)...)
		if got, want := rest(), []speech.UtteranceText{{Utterance: 1, Text: "second", Final: true}}; !slices.Equal(got, want) {
			t.Errorf("%s: then heard %+v, want only %+v", name, got, want)
		}
	}
}

func TestADroppedUtterancesLateTextsAreIgnored(t *testing.T) {
	s := service{answer: script(map[string][]string{
		"input_audio_buffer.clear utterance-0": {`{"type":"conversation.item.input_audio_transcription.delta","item_id":"item_0","delta":"noise"}`},
		"input_audio_buffer.commit commit-1":   strings.Split(second, "|"),
	})}
	send, _, rest := talk(t, s.serve(t))
	send(speech.UtteranceAudio{Samples: make([]int16, 320)}, speech.UtteranceAudio{Drop: true})
	send(utterance(2)...)
	if got, want := rest(), []speech.UtteranceText{{Utterance: 1, Text: "second", Final: true}}; !slices.Equal(got, want) {
		types, _ := s.types(0)
		t.Fatalf("heard %+v, want only %+v; sent %v", got, want, types)
	}
}

func TestAConnectionClosedMidUtteranceFailsItOnceAndTheNextReconnects(t *testing.T) {
	s := service{answer: func(c int, sent map[string]any) []string {
		if c == 1 && sent != nil && sent["type"] == "input_audio_buffer.append" {
			return []string{"close"}
		}
		return script(map[string][]string{"input_audio_buffer.commit commit-1": strings.Split(second, "|")})(c, sent)
	}}
	send, next, rest := talk(t, s.serve(t))
	u := utterance(3)
	send(u[0])
	if h := next(); h.Utterance != 0 || h.Err == nil {
		t.Fatalf("heard %+v, want utterance 0 failed", h)
	}
	send(u[1:]...)
	send(utterance(2)...)
	if got, want := rest(), []speech.UtteranceText{{Utterance: 1, Text: "second", Final: true}}; !slices.Equal(got, want) {
		t.Fatalf("then heard %+v, want %+v", got, want)
	}
}

// A service running behind the client, as under load: each append takes 100 ms to recognise
// while audio comes every 20 ms, the third fails its item, and the audio after it starts a new
// item, which the next commit commits.
func TestAServiceFailingAnItemWhileBehindAnswersEachUtteranceOnce(t *testing.T) {
	appends, failed := 0, false
	s := service{options: realtime.Options{FinalText: 2 * time.Second}, answer: func(c int, sent map[string]any) []string {
		if sent == nil || c > 1 {
			if sent != nil && sent["type"] == "input_audio_buffer.commit" {
				return strings.Split(strings.ReplaceAll(second, "item_1", "item_9"), "|")
			}
			return nil
		}
		item := "item_0"
		if failed {
			item = "item_1"
		}
		switch sent["type"] {
		case "input_audio_buffer.append":
			time.Sleep(100 * time.Millisecond)
			if appends++; appends == 3 {
				failed = true
				return []string{`{"type":"conversation.item.input_audio_transcription.failed","item_id":"item_0","error":{"type":"server_error","code":"server_busy"}}`}
			}
			return []string{`{"type":"conversation.item.input_audio_transcription.delta","item_id":"` + item + `","delta":"leftover "}`}
		case "input_audio_buffer.commit":
			return []string{`{"type":"input_audio_buffer.committed","item_id":"` + item + `"}`,
				`{"type":"conversation.item.input_audio_transcription.completed","item_id":"` + item + `","transcript":"leftover words"}`}
		}
		return nil
	}}
	send, _, rest := talk(t, s.serve(t))
	for _, a := range slices.Concat(utterance(6), utterance(6)) {
		send(a)
		time.Sleep(20 * time.Millisecond)
	}
	answers := map[int]int{}
	for _, h := range rest() {
		if h.Final || h.Err != nil {
			answers[h.Utterance]++
		}
		if h.Utterance == 1 && h.Final && h.Text != "second" {
			t.Errorf("utterance 1's final text is %q, not its own", h.Text)
		}
	}
	if answers[0] != 1 || answers[1] != 1 || len(answers) != 2 {
		t.Fatalf("answers per utterance %v, want one each for utterances 0 and 1", answers)
	}
}
