package kokoro

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mikaclements/voice-guide/internal/speech"
)

// page returns an Ogg page holding segments, each at most 255 bytes; a segment shorter than
// 255 ends its packet, a full one goes on in the next.
func page(segments ...[]byte) []byte {
	b := append([]byte("OggS"), make([]byte, 22)...) // version, type, position, serial, number, checksum
	b = append(b, byte(len(segments)))
	for _, s := range segments {
		b = append(b, byte(len(s)))
	}
	for _, s := range segments {
		b = append(b, s...)
	}
	return b
}

var opusHeaders = slices.Concat(page([]byte("OpusHead\x01\x01\x38\x01")), page([]byte("OpusTags")))

func readAll(stream []byte) ([][]byte, error) {
	var packets [][]byte
	err := readOpus(bytes.NewReader(stream), func(p []byte) bool {
		packets = append(packets, p)
		return true
	})
	return packets, err
}

func TestKokorosOggOpusIsUnwrappedIntoItsTwentyMillisecondPackets(t *testing.T) {
	recorded, err := os.ReadFile("testdata/go-on-please.opus") // Kokoro-FastAPI saying "Go on, please."
	if err != nil {
		t.Fatal(err)
	}
	packets, err := readAll(recorded)
	if err != nil {
		t.Fatal(err)
	}
	// Kokoro puts a second of audio, 50 packets, in each Ogg page; the phrase is 1.32s long.
	if len(packets) != 66 {
		t.Fatalf("%d packets, want 66", len(packets))
	}
	if len(packets[0]) != 327 || !bytes.Equal(packets[65], []byte{0xd8, 0xff, 0xfe}) {
		t.Errorf("first packet %d bytes, last %x; want 327 bytes, then d8fffe", len(packets[0]), packets[65])
	}
}

func TestAPacketContinuedOnTheNextPageIsJoined(t *testing.T) {
	long := append([]byte{0xd8}, bytes.Repeat([]byte{7}, 299)...) // 20 ms of fullband audio, 300 bytes
	stream := slices.Concat(opusHeaders, page(long[:255]), page(long[255:], []byte{0xd8, 1}))
	packets, err := readAll(stream)
	if err != nil || len(packets) != 2 || !bytes.Equal(packets[0], long) || !bytes.Equal(packets[1], []byte{0xd8, 1}) {
		t.Errorf("packets %d (%v), want the 300-byte packet then a 2-byte one", len(packets), err)
	}
}

func TestAStreamThatIsntTwentyMillisecondOpusIsRefused(t *testing.T) {
	full := page(slices.Repeat([][]byte{make([]byte, 255)}, 255)...) // 65,025 bytes of a packet that goes on
	for _, c := range []struct {
		name, want string
		stream     []byte
	}{
		{"not Ogg", "not an Ogg page", []byte("RIFF....WAVEfmt and some more bytes")},
		{"not Opus", "not Opus", slices.Concat(page([]byte("\x01vorbis")), page([]byte("\x03vorbis")))},
		{"10 ms packets", "10ms", slices.Concat(opusHeaders, page([]byte{0xd0, 1}))},                     // fullband, 10 ms
		{"two 20 ms frames in one packet", "40ms", slices.Concat(opusHeaders, page([]byte{0xd9, 1, 2}))}, // fullband, 20 ms, two frames
		{"cut short", "unexpected EOF", slices.Concat(opusHeaders, page([]byte{0xd8, 1})[:28])},
		{"a packet that never ends", "over", slices.Concat(opusHeaders, full, full)},
		{"nothing", "no audio", nil},
		{"headers alone", "no audio", opusHeaders},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := readAll(c.stream); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %v, want one saying %q", err, c.want)
			}
		})
	}
}

// speakFrom returns the speech of one phrase whose answer h writes, with o's deadlines.
func speakFrom(t *testing.T, o Options, h http.HandlerFunc) []speech.Packet {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	text := make(chan string, 1)
	text <- "Tell me about the rebuild."
	close(text)
	var got []speech.Packet
	for s := range New(srv.URL, o).Speak(t.Context(), "af_heart", 1, text) {
		got = append(got, s)
	}
	return got
}

func TestAPhraseOverTheLimitsFailsWithNoneOfItPlayed(t *testing.T) {
	tiny := []byte{0xd8, 1} // a 20 ms packet of fullband audio
	manyTiny := slices.Clone(opusHeaders)
	for left := maxPhrasePackets + 1; left > 0; left -= 255 {
		manyTiny = append(manyTiny, page(slices.Repeat([][]byte{tiny}, min(left, 255))...)...)
	}
	// Packets of 60,000 bytes: each fits in a page of 236 segments, and 70 pass 4 MiB.
	huge := append([]byte{0xd8}, bytes.Repeat([]byte{7}, 59_999)...)
	var segments [][]byte
	for i := 0; i < len(huge); i += 255 {
		segments = append(segments, huge[i:min(i+255, len(huge))])
	}
	fewHuge := slices.Clone(opusHeaders)
	for range 70 {
		fewHuge = append(fewHuge, page(segments...)...)
	}
	for name, c := range map[string]struct {
		stream []byte
		want   string
	}{"too many packets": {manyTiny, "over 3000 packets"}, "too many bytes": {fewHuge, "over 4194304 bytes"}} {
		got := speakFrom(t, Options{}, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(c.stream) })
		if len(got) != 1 || got[0].Err == nil || !strings.Contains(got[0].Err.Error(), c.want) {
			t.Errorf("%s: %d pieces of speech, the last %+v; want only the failure, %q, none of the audio", name, len(got), got[len(got)-1], c.want)
		}
	}
}

func TestAPhraseWhoseRestStallsAfterItsFirstPacketFails(t *testing.T) {
	stopped := make(chan struct{})
	got := speakFrom(t, Options{FirstAudio: 50 * time.Millisecond}, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(slices.Concat(opusHeaders, page([]byte{0xd8, 1})))
		w.(http.Flusher).Flush()
		select { // the first packet came, and the rest never does
		case <-r.Context().Done():
			close(stopped)
		case <-time.After(2 * time.Second):
		}
	})
	if last := got[len(got)-1]; last.Err == nil || !strings.Contains(last.Err.Error(), "rest of the phrase not within 50ms") {
		t.Errorf("speech %+v, want it to end with the stall's error", got)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Error("Kokoro's request went on after the phrase failed")
	}
}
