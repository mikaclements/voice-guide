package kokoro_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mikaclements/voice-guide/internal/speech"
	"github.com/mikaclements/voice-guide/internal/speech/tts/kokoro"
)

// recordedPackets is how many packets the recorded reply holds: "Go on, please." for 1.32s.
const recordedPackets = 66

func recorded(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/go-on-please.opus")
	if err != nil {
		t.Fatalf("read the recorded speech: %v", err)
	}
	return b
}

// serving returns text-to-speech that asks a Kokoro played by h, failing a phrase with no
// audio after a second.
func serving(t *testing.T, h http.HandlerFunc) *kokoro.TextToSpeech {
	return servingWithin(t, time.Second, h)
}

func servingWithin(t *testing.T, firstAudio time.Duration, h http.HandlerFunc) *kokoro.TextToSpeech {
	return servingWith(t, kokoro.Options{FirstAudio: firstAudio}, h)
}

func servingWith(t *testing.T, o kokoro.Options, h http.HandlerFunc) *kokoro.TextToSpeech {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return kokoro.New(srv.URL+"/", o)
}

func TestCuttingBeforeJoiningWordsIsAnOptionOffByDefault(t *testing.T) {
	for _, c := range []struct {
		options kokoro.Options
		want    []string
	}{
		{kokoro.Options{}, []string{"Tell me about a time when you disagreed with your manager."}},
		{kokoro.Options{JoiningWords: true}, []string{"Tell me about a time", "when you disagreed with your manager."}},
	} {
		var asked []string
		rec := recorded(t)
		tts := servingWith(t, c.options, func(w http.ResponseWriter, r *http.Request) {
			asked = append(asked, input(t, r)) // one phrase at a time, so never two at once
			_, _ = w.Write(rec)
		})
		collect(t, tts.Speak(t.Context(), "af_heart", 1, streamed("Tell me about a time when you disagreed with your manager.")))
		if !reflect.DeepEqual(asked, c.want) {
			t.Errorf("with %+v asked for %q, want %q", c.options, asked, c.want)
		}
	}
}

// input returns the text a request asks Kokoro to speak, checking the rest of the request.
func input(t *testing.T, r *http.Request) string {
	var body struct {
		Model, Input, Voice string
		Format              string `json:"response_format"`
		Stream              bool
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.Method != http.MethodPost || r.URL.Path != "/v1/audio/speech" ||
		body.Model != "kokoro" || body.Voice != "af_heart" || body.Format != "opus" || !body.Stream {
		t.Errorf("request %s %s %+v (%v), want a JSON POST to /v1/audio/speech asking for streamed Opus in af_heart", r.Method, r.URL.Path, body, err)
	}
	return body.Input
}

func streamed(pieces ...string) <-chan string {
	text := make(chan string, len(pieces))
	for _, p := range pieces {
		text <- p
	}
	close(text)
	return text
}

// collect takes speech until it ends, failing the test if it doesn't end soon.
func collect(t *testing.T, out <-chan speech.Packet) []speech.Packet {
	t.Helper()
	var all []speech.Packet
	for {
		select {
		case s, ok := <-out:
			if !ok {
				return all
			}
			all = append(all, s)
		case <-time.After(5 * time.Second):
			t.Fatal("the speech didn't end")
		}
	}
}

func TestEachPhraseIsAskedForAndItsPacketsArriveInOrder(t *testing.T) {
	rec := recorded(t)
	tts := serving(t, func(w http.ResponseWriter, r *http.Request) {
		input(t, r)
		_, _ = w.Write(rec)
	})
	got := collect(t, tts.Speak(t.Context(), "af_heart", 1, streamed("Walk me through your work. What did", " you build?")))

	phrases := []speech.Phrase{{Index: 0, Text: "Walk me through your work.", End: 26}, {Index: 1, Text: "What did you build?", End: 46}}
	if len(got) != 2*recordedPackets {
		t.Fatalf("%d packets, want %d", len(got), 2*recordedPackets)
	}
	for i, s := range got {
		want, p := phrases[i/recordedPackets], s.Phrase
		if s.Err != nil || len(s.Opus) == 0 || p.Index != want.Index || p.Text != want.Text || p.End != want.End || p.Ready.IsZero() {
			t.Fatalf("packet %d: %+v, want one of %+v, with the time it was ready", i, s, want)
		}
	}
}

func TestEachPacketSaysHowManyPacketsItsPhraseHas(t *testing.T) {
	rec := recorded(t)
	tts := serving(t, func(w http.ResponseWriter, r *http.Request) {
		input(t, r)
		_, _ = w.Write(rec)
	})
	for i, s := range collect(t, tts.Speak(t.Context(), "af_heart", 1, streamed("Walk me through your work. What did you build?"))) {
		if s.Phrase.Packets != recordedPackets {
			t.Fatalf("packet %d says its phrase has %d packets, want %d, known by the first", i, s.Phrase.Packets, recordedPackets)
		}
	}
}

// window is how long the test holds each step so a phrase asked for too early arrives within
// it and is caught: a request on this machine takes a few milliseconds. A correct Speak passes
// whatever its length, but on a slow machine a Speak that asks too early could slip past it:
// proving that a request never comes needs a hook into Speak, which isn't worth its cost here.
const window = 50 * time.Millisecond

func TestEachPhraseIsAskedForOnceTheOneBeforeIsMadeAndWhileItPlays(t *testing.T) {
	asked, made := make(chan string, 4), make(chan struct{})
	var firstPlayed atomic.Bool
	rec := recorded(t)
	tts := serving(t, func(w http.ResponseWriter, r *http.Request) {
		p := input(t, r)
		switch p {
		case "now tell me more.":
			select {
			case <-made:
			default:
				t.Errorf("asked for %q before the phrase before it was made", p)
			}
		case "What did you build, and why?":
			if !firstPlayed.Load() {
				t.Errorf("asked for %q two phrases ahead of what plays", p)
			}
		}
		asked <- p
		if p == "Thanks for sharing that," {
			select { // Kokoro makes the first phrase until the test says it's made
			case <-made:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = w.Write(rec)
	})
	expect := func(want, when string) {
		t.Helper()
		select {
		case got := <-asked:
			if got != want {
				t.Fatalf("asked for %q %s, want %q", got, when, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("%q wasn't asked for %s", want, when)
		}
	}
	out := tts.Speak(t.Context(), "af_heart", 1, streamed("Thanks for sharing that, now tell me more. What did you build, and why? Take your time with it."))

	expect("Thanks for sharing that,", "first")
	time.Sleep(window) // the first phrase is being made
	close(made)
	take(t, out) // the first phrase starts playing
	expect("now tell me more.", "while the first phrase played")
	time.Sleep(window) // the second phrase is made, the first still plays
	for range recordedPackets - 2 {
		take(t, out)
	}
	firstPlayed.Store(true)
	take(t, out) // the first phrase's last packet
	expect("What did you build, and why?", "once the first phrase had played")
}

// take returns the next piece of speech, failing the test if none comes soon.
func take(t *testing.T, out <-chan speech.Packet) speech.Packet {
	t.Helper()
	select {
	case s := <-out:
		return s
	case <-time.After(time.Second):
		t.Fatal("no speech came")
		return speech.Packet{}
	}
}

func TestTheTextIsReadWhileNobodyTakesTheSpeech(t *testing.T) {
	rec := recorded(t)
	tts := serving(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(rec) })
	text := make(chan string)
	tts.Speak(t.Context(), "af_heart", 1, text)
	for range 10 {
		select {
		case text <- "Tell me more about the rebuild. ":
		case <-time.After(time.Second):
			t.Fatal("the text was held back until its speech was taken")
		}
	}
	close(text)
}

func TestCancellingStopsTheSpeechAndItsRequest(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	tts := serving(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		select {
		case <-r.Context().Done():
			close(stopped)
		case <-time.After(2 * time.Second): // the request went on; end it so the test can
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	out := tts.Speak(ctx, "af_heart", 1, streamed("Tell me about the rebuild."))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Kokoro was never asked")
	}
	cancel()

	if got := collect(t, out); len(got) != 0 {
		t.Errorf("speech after stopping: %+v", got)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Error("Kokoro's request went on after the speech stopped")
	}
}

func TestKokoroRefusingAPhraseEndsTheSpeechWithItsError(t *testing.T) {
	rec := recorded(t)
	tts := serving(t, func(w http.ResponseWriter, r *http.Request) {
		if input(t, r) == "What did you build?" {
			http.Error(w, `{"detail":"voice not found"}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write(rec)
	})
	got := collect(t, tts.Speak(t.Context(), "af_heart", 1, streamed("Walk me through your work. What did you build? Take your time with it.")))

	if len(got) != recordedPackets+1 {
		t.Fatalf("%d pieces of speech, want the first phrase's %d packets, then the error", len(got), recordedPackets)
	}
	last := got[recordedPackets]
	if last.Err == nil || !strings.Contains(last.Err.Error(), "status 400") || last.Phrase.Index != 1 || last.Opus != nil {
		t.Errorf("last %+v, want the second phrase's error, with Kokoro's status", last)
	}
	if last.Err != nil && strings.Contains(last.Err.Error(), "voice not found") {
		t.Errorf("error %q quotes Kokoro's reason, which can hold the user's words", last.Err)
	}
}

func TestAFailureKeepsTheRepliesTextFlowing(t *testing.T) {
	tts := serving(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	})
	text := make(chan string)
	out := tts.Speak(t.Context(), "af_heart", 1, text)
	text <- "Walk me through your work. "
	if got := collect(t, out); len(got) != 1 || got[0].Err == nil {
		t.Fatalf("speech %+v, want one error", got)
	}
	for range 10 {
		select {
		case text <- "Tell me more about the rebuild. ":
		case <-time.After(time.Second):
			t.Fatal("the text was held back after the speech failed")
		}
	}
	close(text)
}

func TestAPhraseWithNoAudioInTimeFails(t *testing.T) {
	stopped := make(chan struct{})
	tts := servingWithin(t, 50*time.Millisecond, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select { // Kokoro took the request and never answers
		case <-r.Context().Done():
			close(stopped)
		case <-time.After(2 * time.Second):
		}
	})
	got := collect(t, tts.Speak(t.Context(), "af_heart", 1, streamed("Walk me through your work.")))

	if len(got) != 1 || got[0].Err == nil || !strings.Contains(got[0].Err.Error(), "no audio within 50ms") {
		t.Fatalf("speech %+v, want one error saying no audio came within 50ms", got)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Error("Kokoro's request went on after the phrase failed")
	}
}

func TestADeadlineOfNoTimeIsTheDefault(t *testing.T) {
	rec := recorded(t)
	for _, firstAudio := range []time.Duration{0, -time.Second} {
		tts := servingWithin(t, firstAudio, func(w http.ResponseWriter, r *http.Request) {
			input(t, r)
			_, _ = w.Write(rec)
		})
		got := collect(t, tts.Speak(t.Context(), "af_heart", 1, streamed("Walk me through your work.")))
		if len(got) != recordedPackets || got[0].Err != nil {
			t.Errorf("with a deadline of %s, %d packets, the first %+v; want %d and no error", firstAudio, len(got), got[0], recordedPackets)
		}
	}
}

// TestLiveKokoroSpeaksRepliesInTimeToPlayThem runs against a real Kokoro-FastAPI when
// TEST_KOKORO_URL names one; .claude/skills/measure/check-tts.sh starts one and runs it. The
// replies are the kind an interviewer asks.
func TestLiveKokoroSpeaksRepliesInTimeToPlayThem(t *testing.T) {
	url := os.Getenv("TEST_KOKORO_URL")
	if url == "" {
		t.Skip("TEST_KOKORO_URL is not set")
	}
	for _, reply := range []string{
		"That sounds like a tough rebuild, so tell me what you changed first and why it mattered to the team.",
		"Can you walk me through that decision and what you would do differently next time?",
		"Tell me about your team and your manager, and how the two of you worked together.",
		"What did you learn after that project shipped late, and what changed on the next one?",
		"Tell me about a time when you disagreed with your manager about a technical decision.",
	} {
		t.Run(reply[:20], func(t *testing.T) { speakLive(t, kokoro.New(url, kokoro.Options{}), reply) })
	}
}

// speakLive streams reply in a word at a time and plays its speech in real time, logging the
// time to first audio and how long playback waited for a later phrase.
func speakLive(t *testing.T, tts *kokoro.TextToSpeech, reply string) {
	text := make(chan string)
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Go(func() {
		defer close(text)
		for _, w := range strings.SplitAfter(reply, " ") {
			text <- w
			time.Sleep(20 * time.Millisecond) // about the model's pace: a reply streams at about 50 words a second
		}
	})

	start := time.Now()
	var first, waited time.Duration
	var due time.Time
	var phrases []string
	for s := range tts.Speak(t.Context(), "af_heart", 1, text) {
		if s.Err != nil {
			t.Fatal(s.Err)
		}
		now := time.Now()
		if due.IsZero() {
			first, due = now.Sub(start), now
		}
		if late := now.Sub(due); late > 0 {
			if late > speech.PacketDuration/2 { // more than scheduling noise: playback would stall
				waited += late
			}
			due = now
		}
		if s.Phrase.Index == len(phrases) {
			phrases = append(phrases, s.Phrase.Text)
		}
		due = due.Add(speech.PacketDuration)
		time.Sleep(time.Until(due)) // played in real time, as the audio track sends it
	}
	t.Logf("first audio %v after the first word; playback waited %v for later phrases; phrases %q", first, waited, phrases)
	if first == 0 {
		t.Error("no speech")
	}
}

func TestEachPhraseIsAskedForInTheSessionsVoiceAtItsSpeed(t *testing.T) {
	for speed, want := range map[float64]any{0.5: 0.5, 1.25: 1.25, 2: 2.0, 0: nil} {
		var body map[string]any
		rec := recorded(t)
		tts := serving(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, _ = w.Write(rec)
		})
		collect(t, tts.Speak(t.Context(), "bf_emma", speed, streamed("Walk me through your work.")))
		if got, sent := body["speed"]; got != want || (want == nil && sent) || body["voice"] != "bf_emma" {
			t.Errorf("asked in %v at speed %v, want bf_emma at %v (nothing for 0, Kokoro's own pace)", body["voice"], body["speed"], want)
		}
	}
}

func TestVoicesAreListedFromKokorosVoicesRoute(t *testing.T) {
	for name, answer := range map[string]string{
		// The MLX server names each voice.
		"names": `{"voices":["af_heart","bf_emma"]}`,
		// Kokoro-FastAPI's image describes each voice, as its answer begins, recorded from the
		// image compose pins.
		"objects": `{"default_voice":"af_heart","voices":[{"id":"af_heart","name":"af_heart","target_quality":"B","training_duration":"MM minutes","overall_grade":"C"},{"id":"bf_emma","name":"bf_emma"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			tts := serving(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/audio/voices" {
					t.Errorf("request %s %s, want GET /v1/audio/voices", r.Method, r.URL.Path)
				}
				_, _ = w.Write([]byte(answer))
			})
			if got, err := tts.Voices(t.Context()); err != nil || !reflect.DeepEqual(got, []string{"af_heart", "bf_emma"}) {
				t.Errorf("Voices = %v, %v; want af_heart and bf_emma", got, err)
			}
		})
	}
}

func TestVoicesFailWhenKokoroIsDownOrAnswersWithoutAList(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"refusing":    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"not JSON":    func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) },
		"not a list":  func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"voices":"af_heart"}`)) },
		"no list":     func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) },
		"a null list": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"voices":null}`)) },
		// A voice that is neither a name nor an object naming it by id can't be named in an agent file.
		"a voice without an id": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"voices":[{"name":"af_heart"}]}`))
		},
		"a number for a voice": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"voices":[3]}`)) },
		"an empty name":        func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"voices":[""]}`)) },
		"a null voice":         func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"voices":[null]}`)) },
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := serving(t, h).Voices(t.Context()); err == nil {
				t.Errorf("Voices = %v, want an error", got)
			}
		})
	}
	srv := httptest.NewServer(nil)
	srv.Close()
	if _, err := kokoro.New(srv.URL, kokoro.Options{}).Voices(t.Context()); err == nil {
		t.Error("Voices with nothing listening, want an error")
	}
}

func TestAnEmptyListOfVoicesIsAnswered(t *testing.T) {
	tts := serving(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"voices":[]}`)) })
	if got, err := tts.Voices(t.Context()); err != nil || len(got) != 0 {
		t.Errorf("Voices = %v, %v; want an empty list and no error", got, err)
	}
}
