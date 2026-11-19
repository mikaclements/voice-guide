package webrtc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/pion/opus/pkg/oggreader"
	"github.com/pion/rtp"

	"github.com/mikaclements/voice-guide/internal/speech"
)

// recorded returns the audio packets of a recording of 20 ms packets, made
// as the microphone tests say.
func recorded(t *testing.T) [][]byte {
	t.Helper()
	b, err := os.ReadFile("testdata/can-you-tell-me-more.opus")
	if err != nil {
		t.Fatalf("read the recording: %v", err)
	}
	ogg, _, err := oggreader.NewWith(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("read the recording: %v", err)
	}
	var packets [][]byte
	for {
		p, _, err := ogg.ParseNextPacket()
		if errors.Is(err, io.EOF) {
			return packets[2:] // after the two headers
		}
		if err != nil {
			t.Fatalf("read the recording: %v", err)
		}
		packets = append(packets, p)
	}
}

func TestAudioSpeechIsTooSlowToTakeIsDroppedButCountsAsTime(t *testing.T) {
	var logged bytes.Buffer
	audio := make(chan speech.UserAudio, listenAhead)
	mic, err := newMicrophone(audio, slog.New(slog.NewJSONHandler(&logged, nil)))
	if err != nil {
		t.Fatalf("newMicrophone: %v", err)
	}
	take := func(i int, payload []byte) {
		mic.take(&rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(i), Timestamp: uint32(i * 960)}, Payload: payload})
	}
	packets := recorded(t)
	last := len(packets) - 1
	for i, p := range packets[:last] {
		take(i, p)
	}
	// Nothing took the audio: a second of it is held, and the rest dropped.
	if len(audio) != listenAhead || listenAhead*listenStep != speech.ListenRate {
		t.Fatalf("%d stretches of %d samples held, want a second's worth", len(audio), listenStep)
	}
	for range listenAhead {
		<-audio
	}
	take(last, packets[last])
	a := <-audio
	if dropped := (last - listenAhead) * listenStep; a.Missing != dropped || len(a.PCM) != listenStep {
		t.Errorf("the next stretch has %d samples after %d missing; want 320 after the %d dropped", len(a.PCM), a.Missing, dropped)
	}
	mic.end()
	var ended struct {
		DroppedMS int `json:"dropped_ms"`
	}
	lines := bytes.Split(bytes.TrimSpace(logged.Bytes()), []byte("\n"))
	if err := json.Unmarshal(lines[len(lines)-1], &ended); err != nil || ended.DroppedMS != (last-listenAhead)*20 {
		t.Errorf("logged %s at the end, want %d ms dropped", lines[len(lines)-1], (last-listenAhead)*20)
	}
}
