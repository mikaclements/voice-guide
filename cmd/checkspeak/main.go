// Command checkspeak talks to an agent the way a browser does, with no one at the microphone: it
// starts a session, opens its audio connection over WebRTC, and says each sentence into it as
// Opus that Kokoro made, at the pace it is spoken; then it prints what voice heard, the reply, and
// the time from the sentence's last packet to the reply's first audio packet. Given -greetings, it
// instead starts that many sessions of an agent that opens and times each greeting's first audio
// packet from the session's start and from its audio connection coming up. It wires only what it needs: a product's key to
// start the session and the ticket that start hands back, and Kokoro's adapter. Use it through
// .claude/skills/measure/check-speak.sh.
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/mikaclements/voice-guide/internal/speech"
	"github.com/mikaclements/voice-guide/internal/speech/tts/kokoro"
)

// What the user says, a sentence a turn, in turn.
var sentences = []string{
	"Hi, I'm ready. Please start the interview.",
	"I led the rebuild of shipment tracking at Harbourline: status updates moved from nightly batch files to an event stream.",
	"The hardest part was keeping the read model consistent when events arrived out of order.",
}

// After the turns, the user asks, then cuts in on the reply's start; the second sentence opens
// with no pause, so it is held from its first word.
const (
	asking = "What would you like to hear about next?"
	cutIn  = "Actually I want to add something about the migration first."
)

// silence is one 20 ms Opus packet of silence, which a microphone sends between sentences, so
// voice hears the pause after each.
var silence = []byte{0xf8, 0xff, 0xfe}

const (
	// quietBefore is the silence sent before each sentence: half a second, as a person draws breath,
	// so voice has the audio from before the speech began.
	quietBefore = 25
	// replyWait bounds the wait for a reply to play to its end: voice cuts a turn off at 20 s.
	replyWait = 30 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "checkspeak:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string) error {
	fs := flag.NewFlagSet("checkspeak", flag.ContinueOnError)
	voice := fs.String("voice", "http://"+cmp.Or(getenv("VOICE_ADDR"), "127.0.0.1:8080"), "voice's address")
	kokoroURL := fs.String("kokoro", cmp.Or(getenv("VOICE_TTS_URL"), "http://127.0.0.1:8880"), "Kokoro-FastAPI's address, which says the sentences")
	agent := fs.String("agent", "01a1de84-fbfc-7758-9af4-3ced6d8a90cc", "the agent to talk to (the mock interviewer)")
	resume := fs.String("resume", "seed/placeholder-user/resume.md", "the resume sent as session context")
	reference := fs.String("reference", "seed/placeholder-user/reference.txt", "the file holding the product's reference for the user")
	turns := fs.Int("turns", 3, "how many sentences to say")
	greetings := fs.Int("greetings", 0, "instead of speaking, start this many sessions and time each greeting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	productKey := getenv("VOICE_DEV_PRODUCT_KEY")
	if productKey == "" {
		return errors.New("VOICE_DEV_PRODUCT_KEY is not set")
	}
	join := func(note string) (*browser, string, error) {
		session, ticket, err := start(ctx, *voice, productKey, *agent, *reference, *resume, note)
		if err != nil {
			return nil, "", err
		}
		b, err := open(ctx, client{base: *voice, ticket: ticket}, session)
		return b, session, err
	}
	if *greetings > 0 {
		return greet(ctx, *greetings, join)
	}
	b, _, err := join("")
	if err != nil {
		return err
	}
	defer b.close(ctx)
	tts := kokoro.New(*kokoroURL, kokoro.Options{})
	for i := range *turns {
		said := sentences[i%len(sentences)]
		packets, err := say(ctx, tts, said)
		if err != nil {
			return err
		}
		r, err := b.speak(ctx, packets)
		if err != nil {
			return fmt.Errorf("turn %d: %w", i+1, err)
		}
		// Negative when voice took a turn before the sentence ended, at a pause inside it.
		fmt.Printf("turn %d\n  said:  %s\n  heard: %s\n  reply: %s\n  last packet to first agent audio: %d ms (voice: speech end to first audio sent %d ms)\n",
			i+1, said, strings.Join(r.heard, " | "), r.reply.String(), r.firstAudio.Milliseconds(), r.voiceMS)
		if len(r.heard) > 1 {
			fmt.Println("  voice took a turn at a pause inside the sentence, and the rest interrupted its reply")
		}
		if r.talkedOn > 0 {
			fmt.Printf("  voice took a turn at a pause inside the sentence, and the rest talked on: one turn (%d times)\n", r.talkedOn)
		}
	}
	first, err := say(ctx, tts, asking)
	if err != nil {
		return err
	}
	over, err := say(ctx, tts, cutIn)
	if err != nil {
		return err
	}
	r, stopped, err := b.interrupt(ctx, first, over)
	if err != nil {
		return fmt.Errorf("cutting in: %w", err)
	}
	fmt.Printf("cutting in\n  said:  %s\n  then, as its reply began: %s\n  heard: %s\n  reply: %s\n  first packet said over the reply to the reply's last audio packet: %d ms\n",
		asking, cutIn, strings.Join(r.heard, " | "), r.reply.String(), stopped.Milliseconds())
	return nil
}

// greet joins n sessions in turn and, in each, asks for the agent's opening as the page does, then
// prints the greeting and the time to its first audio packet from the session's start and from the
// audio connection coming up. Session 1 is the coldest and is reported apart; then the median and
// 90th percentile of the rest.
func greet(ctx context.Context, n int, join func(note string) (*browser, string, error)) error {
	var fromStart, fromUp []time.Duration
	for i := range n {
		began := time.Now()
		// A line of its own in each session's context, so each greeting reads its session's layers
		// afresh, as every real session does; the agent's layer may come from cache, as it does in use.
		b, session, err := join(fmt.Sprintf("(Speed check session %s.)", uuid.NewV7()))
		if err != nil {
			return err
		}
		r, err := b.opening(ctx)
		b.close(ctx)
		if err != nil {
			return fmt.Errorf("session %d: %w", i+1, err)
		}
		fromStart = append(fromStart, b.up.Add(r.firstAudio).Sub(began))
		fromUp = append(fromUp, r.firstAudio)
		fmt.Printf("session %d (%s)\n  greeting: %s\n  session start to first audio: %d ms, audio up to first audio: %d ms\n",
			i+1, session, r.reply.String(), fromStart[i].Milliseconds(), r.firstAudio.Milliseconds())
	}
	fmt.Printf("greetings, session 1, the coldest: session start to first audio %d ms, audio up to first audio %d ms\n",
		fromStart[0].Milliseconds(), fromUp[0].Milliseconds())
	if n > 1 {
		for _, m := range []struct {
			name  string
			times []time.Duration
		}{{"session start to first audio", fromStart[1:]}, {"audio up to first audio", fromUp[1:]}} {
			slices.Sort(m.times)
			fmt.Printf("greetings, sessions 2 to %d: %s median %d ms, 90th percentile %d ms\n",
				n, m.name, percentile(m.times, 50).Milliseconds(), percentile(m.times, 90).Milliseconds())
		}
	}
	return nil
}

// percentile is the nearest-rank percentile of sorted, which holds at least one value: a time
// one of the sessions took, never one between two.
func percentile(sorted []time.Duration, p int) time.Duration {
	return sorted[(len(sorted)*p+99)/100-1]
}

// say returns the sentence as Kokoro speaks it, in 20 ms Opus packets.
func say(ctx context.Context, tts *kokoro.TextToSpeech, sentence string) ([][]byte, error) {
	text := make(chan string, 1)
	text <- sentence
	close(text)
	var packets [][]byte
	for p := range tts.Speak(ctx, "af_heart", text) {
		if p.Err != nil {
			return nil, fmt.Errorf("say the sentence: %w", p.Err)
		}
		packets = append(packets, p.Opus)
	}
	return packets, nil
}

// client calls voice's API as the browser does, with the ticket the start handed back, which it
// reuses for every request, as the page does.
type client struct {
	base   string
	ticket string
}

func (c client) do(ctx context.Context, method, path, contentType string, body []byte) (*http.Response, error) {
	return send(ctx, c.base, c.ticket, method, path, contentType, body)
}

func send(ctx context.Context, base, bearer, method, path, contentType string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", contentType)
	return http.DefaultClient.Do(req)
}

// start starts a session with the agent as a product's server does: with its key, naming the user
// by the reference in a file and sending the resume as session context. It returns the session's
// ID and its ticket, which the browser's side of the check then uses. A note, when given, ends the
// resume.
func start(ctx context.Context, base, productKey, agent, referenceFile, resumeFile, note string) (id, ticket string, err error) {
	resume, err := os.ReadFile(resumeFile)
	if err != nil {
		return "", "", err
	}
	if note != "" {
		resume = fmt.Appendf(resume, "\n\n%s\n", note)
	}
	reference, err := os.ReadFile(referenceFile)
	if err != nil {
		return "", "", err
	}
	body, _ := json.Marshal(map[string]any{"agent_id": agent, "request_id": uuid.NewV7().String(),
		"user":            strings.TrimSpace(string(reference)),
		"session_context": []map[string]string{{"name": "resume", "content": string(resume)}}})
	res, err := send(ctx, base, productKey, http.MethodPost, "/v1/sessions", "application/json", body)
	if err != nil {
		return "", "", fmt.Errorf("start a session: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	var s struct {
		ID     string `json:"id"`
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil || res.StatusCode/100 != 2 || s.Ticket == "" {
		return "", "", fmt.Errorf("start a session: status %d", res.StatusCode)
	}
	return s.ID, s.Ticket, nil
}

// browser is one audio connection's browser side: its microphone, when its next packet is due, the
// messages voice sends on its data channel, and when each of the agent's audio packets arrived.
type browser struct {
	api      client
	pc       *webrtc.PeerConnection
	mic      *webrtc.TrackLocalStaticSample
	next     time.Time
	location string
	turns    *webrtc.DataChannel
	// When the data channel opened: the audio connection is up, and the page asks for the opening.
	up       time.Time
	messages chan message
	arrivals chan time.Time
}

// message is what voice sends on the data channel, as far as the check reads it.
type message struct {
	Type         string `json:"type"`
	Text         string `json:"text"`
	Final        bool   `json:"final"`
	Reason       string `json:"reason"`
	Failed       bool   `json:"failed"`
	FirstAudioMS int64  `json:"first_audio_ms"`
}

// open opens the session's audio connection, with the microphone sending.
func open(ctx context.Context, api client, session string) (*browser, error) {
	codecs := &webrtc.MediaEngine{}
	if err := codecs.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}
	var settings webrtc.SettingEngine
	settings.SetIncludeLoopbackCandidate(true)
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(codecs), webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, err
	}
	// Bounds hold more than a turn brings: a reply is a few hundred messages and packets a second.
	b := &browser{api: api, pc: pc, messages: make(chan message, 1024), arrivals: make(chan time.Time, 1024)}
	if b.mic, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "microphone"); err != nil {
		return nil, b.fail(err)
	}
	if _, err := pc.AddTrack(b.mic); err != nil {
		return nil, b.fail(err)
	}
	// Run by Pion, which GracefulClose waits for.
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			if _, _, err := track.ReadRTP(); err != nil {
				return
			}
			select {
			case b.arrivals <- time.Now():
			default:
			}
		}
	})
	if b.turns, err = pc.CreateDataChannel("turns", nil); err != nil {
		return nil, b.fail(err)
	}
	opened := make(chan struct{})
	b.turns.OnOpen(func() {
		b.up = time.Now()
		close(opened)
	})
	b.turns.OnMessage(func(m webrtc.DataChannelMessage) {
		var got message
		if json.Unmarshal(m.Data, &got) == nil {
			select {
			case b.messages <- got:
			default:
			}
		}
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return nil, b.fail(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return nil, b.fail(err)
	}
	<-gathered
	res, err := api.do(ctx, http.MethodPost, "/v1/sessions/"+session+"/audio", "application/sdp", []byte(pc.LocalDescription().SDP))
	if err != nil {
		return nil, b.fail(err)
	}
	answer, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		return nil, b.fail(fmt.Errorf("open the audio connection: status %d", res.StatusCode))
	}
	b.location = res.Header.Get("Location")
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: string(answer)}); err != nil {
		return nil, b.fail(err)
	}
	select {
	case <-opened:
		return b, nil
	case <-time.After(10 * time.Second):
		return nil, b.fail(errors.New("the data channel didn't open"))
	}
}

func (b *browser) fail(err error) error {
	_ = b.pc.GracefulClose()
	return err
}

// close ends the connection, as the page does when it closes.
func (b *browser) close(ctx context.Context) {
	if b.location != "" {
		if res, err := b.api.do(ctx, http.MethodDelete, b.location, "", nil); err == nil {
			_ = res.Body.Close()
		}
	}
	_ = b.pc.GracefulClose()
}

// outcome is what came of saying one sentence: the words each turn was taken with, the reply,
// the time from the sentence's last packet to the reply's first audio packet, voice's own time
// from the end of the speech to its first audio sent, when each of the agent's packets arrived,
// whether a reply was interrupted, and how often the user talked on before a reply reached them.
type outcome struct {
	heard       []string
	reply       strings.Builder
	firstAudio  time.Duration
	voiceMS     int64
	arrived     []time.Time
	interrupted bool
	talkedOn    int
}

// send sends one packet as the microphone, at the pace packets play.
func (b *browser) send(ctx context.Context, p []byte) error {
	select {
	case <-time.After(time.Until(b.next)):
	case <-ctx.Done():
		return ctx.Err()
	}
	b.next = b.next.Add(speech.PacketDuration)
	return b.mic.WriteSample(media.Sample{Data: p, Duration: speech.PacketDuration})
}

// speak sends the packets as the microphone at the pace they play, between silences, and keeps
// the microphone sending silence until the reply has played.
func (b *browser) speak(ctx context.Context, packets [][]byte) (*outcome, error) {
	_, _ = b.drain(&outcome{}, time.Time{}) // nothing from before counts
	b.next = time.Now()
	for range quietBefore {
		if err := b.send(ctx, silence); err != nil {
			return nil, err
		}
	}
	for _, p := range packets {
		if err := b.send(ctx, p); err != nil {
			return nil, err
		}
	}
	return b.listen(ctx, &outcome{}, time.Now())
}

// opening asks for the agent's opening on the data channel, as the page does once the audio
// connection is up, and keeps the microphone sending silence until the greeting has played, its
// first audio packet timed from the connection coming up.
func (b *browser) opening(ctx context.Context) (*outcome, error) {
	ask, _ := json.Marshal(map[string]any{"type": "turn", "request_id": uuid.NewV7().String(), "opening": true})
	if err := b.turns.SendText(string(ask)); err != nil {
		return nil, fmt.Errorf("ask for the opening: %w", err)
	}
	b.next = time.Now()
	return b.listen(ctx, &outcome{}, b.up)
}

// listen keeps the microphone sending silence until the reply has played; the sentence before
// ended at last.
func (b *browser) listen(ctx context.Context, r *outcome, last time.Time) (*outcome, error) {
	for time.Since(last) < replyWait {
		if err := b.send(ctx, silence); err != nil {
			return nil, err
		}
		if done, err := b.drain(r, last); done || err != nil {
			return r, err
		}
	}
	return nil, fmt.Errorf("no reply played within %s", replyWait)
}

// interrupt says first, then over as soon as first's reply is heard, and listens until the reply
// to over has played. It returns what came of over, and how long after over's first packet went the
// last packet of first's reply arrived.
func (b *browser) interrupt(ctx context.Context, first, over [][]byte) (*outcome, time.Duration, error) {
	_, _ = b.drain(&outcome{}, time.Time{})
	b.next = time.Now()
	for _, p := range slices.Concat(slices.Repeat([][]byte{silence}, quietBefore), first) {
		if err := b.send(ctx, p); err != nil {
			return nil, 0, err
		}
	}
	asked, last := &outcome{}, time.Now()
	for asked.firstAudio == 0 {
		if time.Since(last) > replyWait {
			return nil, 0, fmt.Errorf("no reply played within %s", replyWait)
		}
		if err := b.send(ctx, silence); err != nil {
			return nil, 0, err
		}
		if done, err := b.drain(asked, last); done || err != nil {
			return nil, 0, cmp.Or(err, errors.New("the reply ended before it was cut in on"))
		}
	}
	began, r := b.next, &outcome{}
	for _, p := range over {
		if err := b.send(ctx, p); err != nil {
			return nil, 0, err
		}
		if _, err := b.drain(r, time.Time{}); err != nil {
			return nil, 0, err
		}
	}
	if _, err := b.listen(ctx, r, time.Now()); err != nil {
		return nil, 0, err
	}
	if !r.interrupted {
		return nil, 0, errors.New("the reply played to its end: speaking over it didn't cut in")
	}
	// The interrupted reply's packets come every 20 ms until it stops; the next reply's come a second
	// or more later, after the rest of the speech, its pause and the model, so a gap of 200 ms, ten
	// packets' worth and more than the network's jitter, ends the first.
	stopped := began
	for _, at := range r.arrived {
		if at.Sub(stopped) > 200*time.Millisecond {
			break
		}
		if at.After(stopped) {
			stopped = at
		}
	}
	return r, stopped.Sub(began), nil
}

// drain takes what voice sent so far, and says whether the reply has played.
func (b *browser) drain(r *outcome, last time.Time) (bool, error) {
	for {
		select {
		case at := <-b.arrivals:
			r.arrived = append(r.arrived, at)
			if r.firstAudio == 0 && !last.IsZero() {
				r.firstAudio = at.Sub(last)
			}
		case m := <-b.messages:
			switch m.Type {
			case "hearing":
				if m.Final && m.Text != "" {
					r.heard = append(r.heard, m.Text)
				}
			case "text":
				r.reply.WriteString(m.Text)
			case "interrupted":
				r.interrupted = true
			case "talking_on": // the turn taken is taken back, and taken again with all the words
				r.talkedOn++
				r.heard = r.heard[:max(len(r.heard)-1, 0)]
				r.reply.Reset()
			case "spoken":
				r.voiceMS = m.FirstAudioMS
				if m.Failed && r.firstAudio == 0 {
					return true, errors.New("the reply played no audio: text-to-speech failed")
				}
				return true, nil
			case "refused", "failed":
				return true, fmt.Errorf("the turn was %s %s", m.Type, m.Reason)
			}
		default:
			return false, nil
		}
	}
}
