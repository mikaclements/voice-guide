package webrtc_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	pionopus "github.com/pion/opus"
	"github.com/pion/opus/pkg/oggreader"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/mikaclements/voice-guide/internal/speech"
)

// newPeerWithMicrophone is a browser that sends its microphone as well as
// receiving the agent's audio, as a page that was given the microphone does.
func newPeerWithMicrophone(t *testing.T) *peer {
	t.Helper()
	p := newPeer(t, false)
	mic, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "microphone")
	if err != nil {
		t.Fatalf("NewTrackLocalStaticRTP: %v", err)
	}
	if _, err := p.pc.AddTrack(mic); err != nil {
		t.Fatalf("AddTrack: %v", err)
	}
	p.mic = mic
	return p
}

// The recordings are Kokoro saying "Can you tell me more about that?", 2.34
// s, encoded with libopus 1.6.1 as Chrome's WebRTC encoder is set up (the
// VOIP application, mono, complexity 9).
const (
	// In 20 ms packets: a third at 32 kbps, a third at 12 kbps as on a
	// congested network, and a third at 64 kbps with the signal marked as
	// music, so it holds hybrid, SILK and CELT packets, in that order.
	everyMode = "testdata/can-you-tell-me-more.opus"
	// In packets of 10, 20, 40 and 60 ms in turn: half at 32 kbps, hybrid,
	// where 40 and 60 ms are several 20 ms frames in one packet, and half at
	// 12 kbps, SILK, where they are frames of their own.
	everyLength = "testdata/can-you-tell-me-more-mixed.opus"
)

// packet is one of a recording's packets as RTP carries it.
type packet struct {
	seq     uint16
	ts      uint32
	payload []byte
	samples int // at 16 kHz
}

// recording returns a recording's audio packets, numbered and stamped as a
// browser sends them: the timestamp counts audio at Opus's 48 kHz.
func recording(t *testing.T, name string) []packet {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read the recording: %v", err)
	}
	ogg, _, err := oggreader.NewWith(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("read the recording: %v", err)
	}
	decoder, err := pionopus.NewDecoderWithOutput(speech.ListenRate, 1)
	if err != nil {
		t.Fatalf("NewDecoderWithOutput: %v", err)
	}
	pcm := make([]int16, 1920)
	var packets []packet
	var ts uint32
	for {
		payload, _, err := ogg.ParseNextPacket()
		if errors.Is(err, io.EOF) {
			return packets
		}
		if err != nil {
			t.Fatalf("read the recording: %v", err)
		}
		if bytes.HasPrefix(payload, []byte("OpusHead")) || bytes.HasPrefix(payload, []byte("OpusTags")) {
			continue
		}
		n, err := decoder.DecodeToInt16(payload, pcm)
		if err != nil {
			t.Fatalf("decode the recording: %v", err)
		}
		packets = append(packets, packet{seq: uint16(len(packets)), ts: ts, payload: payload, samples: n})
		ts += uint32(n * 3)
	}
}

// decoded is the packets decoded in the order given, by a decoder that saw
// only them: what the transport should yield of them.
func decoded(t *testing.T, packets []packet) []int16 {
	t.Helper()
	decoder, err := pionopus.NewDecoderWithOutput(speech.ListenRate, 1)
	if err != nil {
		t.Fatalf("NewDecoderWithOutput: %v", err)
	}
	pcm := make([]int16, 1920)
	var all []int16
	for _, p := range packets {
		n, err := decoder.DecodeToInt16(p.payload, pcm)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		all = append(all, pcm[:n]...)
	}
	return all
}

// speak sends the packets on the peer's microphone track in the order given,
// as fast as they go, and reports the first failure, or nil once all are sent.
func speak(p *peer, packets []packet) <-chan error {
	done := make(chan error, 1)
	go func() {
		for _, pk := range packets {
			if err := p.mic.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: pk.seq, Timestamp: pk.ts}, Payload: pk.payload}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	return done
}

// listened is what came of the microphone: each stretch, all the audio in order,
// and the missing audio by where it fell in it.
type listened struct {
	stretches []speech.UserAudio
	pcm       []int16
	missing   map[int]int
}

// hear reads the microphone audio from conn until it has n samples of time,
// counting what is missing.
func hear(t *testing.T, conn speech.Connection, n int) listened {
	t.Helper()
	h := listened{missing: map[int]int{}}
	for got := 0; got < n; {
		select {
		case a := <-conn.Listen():
			h.stretches = append(h.stretches, a)
			if a.Missing > 0 {
				h.missing[len(h.pcm)] += a.Missing
			}
			h.pcm = append(h.pcm, a.PCM...)
			got += a.Missing + len(a.PCM)
		case <-time.After(wait):
			t.Fatalf("waited %s for the microphone audio: %d of %d samples came", wait, len(h.pcm), n)
		}
	}
	return h
}

// samples counts the packets' audio at 16 kHz.
func samples(packets []packet) int {
	n := 0
	for _, p := range packets {
		n += p.samples
	}
	return n
}

// reference is the every-mode recording decoded by libopus 1.6.1 at 16 kHz
// mono, the reference implementation of Opus (RFC 6716).
func reference(t *testing.T) []int16 {
	t.Helper()
	b, err := os.ReadFile("testdata/can-you-tell-me-more-16k.pcm")
	if err != nil {
		t.Fatalf("read the reference: %v", err)
	}
	pcm := make([]int16, len(b)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return pcm
}

// logs keeps what a transport logs, as JSON, for a test to read once the
// connection has ended.
type logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logs) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(b)
}

// records returns the records logged with the message msg.
func (l *logs) records(t *testing.T, msg string) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var found []map[string]any
	for line := range strings.Lines(l.buf.String()) {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("a log line isn't JSON: %s", line)
		}
		if r["msg"] == msg {
			found = append(found, r)
		}
	}
	return found
}

// sent waits for speak to finish.
func sent(t *testing.T, done <-chan error) {
	t.Helper()
	if err := <-done; err != nil {
		t.Fatalf("send the recording: %v", err)
	}
}

// minSNR is how close to libopus the microphone audio must come, as a ratio
// of the reference's power to that of the difference. pion/opus came to 47.8
// dB on the recording's SILK third, its worst, and 50.1 dB over all of it; a
// packet decoded wrongly, lost or out of order brings it near 0 dB.
const minSNR = 40

func TestTheMicrophoneTrackComesOutAsTheReferenceDecodesItAt16kHz(t *testing.T) {
	p := newPeerWithMicrophone(t)
	conn := connect(t, newTransport(t), p)
	want := reference(t)
	done := speak(p, recording(t, everyMode))
	got := hear(t, conn, len(want))
	sent(t, done)
	if len(got.pcm) != len(want) || len(got.missing) != 0 {
		t.Fatalf("%d samples came, %v missing; want %d, none missing", len(got.pcm), got.missing, len(want))
	}
	var signal, noise float64
	for i := range want {
		d := float64(got.pcm[i]) - float64(want[i])
		signal += float64(want[i]) * float64(want[i])
		noise += d * d
	}
	if snr := 10 * math.Log10(signal/noise); snr < minSNR {
		t.Errorf("the microphone audio is %.1f dB from libopus's, want at least %d dB", snr, minSNR)
	}
}

func TestPacketsOfEveryLengthComeOutIn20msStretches(t *testing.T) {
	packets := recording(t, everyLength)
	lengths := map[int]bool{}
	for _, pk := range packets {
		lengths[pk.samples] = true
	}
	if !lengths[160] || !lengths[320] || !lengths[640] || !lengths[960] {
		t.Fatalf("the recording has packets of %v samples, want 10, 20, 40 and 60 ms", lengths)
	}
	p := newPeerWithMicrophone(t)
	conn := connect(t, newTransport(t), p)
	done := speak(p, packets)
	got := hear(t, conn, samples(packets))
	sent(t, done)
	for i, a := range got.stretches {
		if len(a.PCM) != 320 || a.Missing != 0 {
			t.Fatalf("stretch %d holds %d samples, %d missing; want 320, 20 ms, none missing", i, len(a.PCM), a.Missing)
		}
	}
	if !slices.Equal(got.pcm, decoded(t, packets)) {
		t.Error("the audio differs from the packets decoded in order")
	}
}

func TestLostPacketsCountAsTheAudioTheyHeld(t *testing.T) {
	packets := recording(t, everyMode)
	// Three 20 ms packets lost: 960 samples, where the 41st began.
	kept := slices.Concat(packets[:40], packets[43:])
	p := newPeerWithMicrophone(t)
	conn := connect(t, newTransport(t), p)
	done := speak(p, kept)
	got := hear(t, conn, samples(packets))
	sent(t, done)
	if want := map[int]int{40 * 320: 960}; !maps.Equal(got.missing, want) {
		t.Errorf("missing %v, want %v", got.missing, want)
	}
	if !slices.Equal(got.pcm, decoded(t, kept)) {
		t.Error("the audio differs from the packets that came, decoded in order")
	}
}

func TestDuplicatePacketsAndPacketsTooLateAreDropped(t *testing.T) {
	packets := recording(t, everyMode)
	// The 6th comes twice; the 21st after the 22nd, too late, so it counts
	// as missing.
	order := slices.Concat(packets[:6], packets[5:20], packets[21:22], packets[20:21], packets[22:])
	p := newPeerWithMicrophone(t)
	conn := connect(t, newTransport(t), p)
	done := speak(p, order)
	got := hear(t, conn, samples(packets))
	sent(t, done)
	if want := map[int]int{20 * 320: 320}; !maps.Equal(got.missing, want) {
		t.Errorf("missing %v, want %v", got.missing, want)
	}
	if !slices.Equal(got.pcm, decoded(t, slices.Concat(packets[:20], packets[21:]))) {
		t.Error("the audio differs from each packet in time decoded once, in order")
	}
}

func TestATimestampJumpCountsAsAtMostTenSecondsMissing(t *testing.T) {
	packets := recording(t, everyMode)[:2]
	packets[1].ts += 48000 * 3600 // an hour on
	p := newPeerWithMicrophone(t)
	conn := connect(t, newTransport(t), p)
	done := speak(p, packets)
	got := hear(t, conn, 2*320+10*speech.ListenRate)
	sent(t, done)
	if want := map[int]int{320: 10 * speech.ListenRate}; !maps.Equal(got.missing, want) {
		t.Errorf("missing %v, want %v: ten seconds where the jump fell", got.missing, want)
	}
}

// renumbered returns the packets numbered on from seq and stamped on from ts,
// as a browser that started there sends them.
func renumbered(packets []packet, seq uint16, ts uint32) []packet {
	out := slices.Clone(packets)
	for i := range out {
		out[i].seq += seq
		out[i].ts += ts
	}
	return out
}

func TestNumbersAndTimestampsThatWrapAroundKeepTime(t *testing.T) {
	// Numbered from 65500 and stamped a second before the timestamp wraps:
	// the numbers wrap at the 37th packet and the timestamps at the 51st, and
	// the 41st to 43rd are lost.
	packets := renumbered(recording(t, everyMode), 65500, 1<<32-48000)
	kept := slices.Concat(packets[:40], packets[43:])
	p := newPeerWithMicrophone(t)
	conn := connect(t, newTransport(t), p)
	done := speak(p, kept)
	got := hear(t, conn, samples(packets))
	sent(t, done)
	if want := map[int]int{40 * 320: 960}; !maps.Equal(got.missing, want) {
		t.Errorf("missing %v, want %v", got.missing, want)
	}
	if !slices.Equal(got.pcm, decoded(t, kept)) {
		t.Error("the audio differs from the packets that came, decoded in order")
	}
}

func TestNumbersThatJumpFarBackAreTakenOnceTwoComeInOrder(t *testing.T) {
	// From the 11th packet on, the numbers jump 40000 on, which reads as far
	// behind: the 11th is dropped, and the 12th, in order after it, shows the
	// browser restarted its numbering.
	packets := recording(t, everyMode)
	jumped := slices.Concat(packets[:10], renumbered(packets[10:], 40000, 0))
	p := newPeerWithMicrophone(t)
	conn := connect(t, newTransport(t), p)
	done := speak(p, jumped)
	got := hear(t, conn, samples(packets))
	sent(t, done)
	if want := map[int]int{10 * 320: 320}; !maps.Equal(got.missing, want) {
		t.Errorf("missing %v, want %v: the 11th packet's 20 ms", got.missing, want)
	}
	if !slices.Equal(got.pcm, decoded(t, slices.Concat(packets[:10], packets[11:]))) {
		t.Error("the audio differs from every packet but the 11th, decoded in order")
	}
}

func TestEachOpusModeIsLoggedTheFirstTimeAConnectionSendsIt(t *testing.T) {
	var l logs
	p := newPeerWithMicrophone(t)
	conn := connect(t, newTransportLogging(t, slog.New(slog.NewJSONHandler(&l, nil))), p)
	packets := recording(t, everyMode)
	done := speak(p, packets)
	hear(t, conn, samples(packets))
	sent(t, done)
	conn.Close(speech.Ended)
	var modes []any
	for _, r := range l.records(t, "microphone audio") {
		modes = append(modes, r["mode"])
	}
	if want := []any{"hybrid", "silk", "celt"}; !slices.Equal(modes, want) {
		t.Errorf("logged the modes %v, want %v: each once, as it first came", modes, want)
	}
}
