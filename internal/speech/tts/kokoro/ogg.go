package kokoro

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/mikaclements/voice-guide/internal/speech"
)

// maxPacketBytes bounds one packet; the adapter bounds a whole phrase. An Opus frame is at most
// 1,275 bytes and Kokoro's headers a few dozen, so a longer packet means a broken stream, and
// reading on would only fill memory.
const maxPacketBytes = 64 << 10

// readOpus reads an Ogg Opus stream (RFC 7845) and hands each audio packet to packet as soon as
// its page arrives, after checking the stream's two header packets. Kokoro puts a second of
// audio, 50 packets, in each page, so packets are split by the page's segment sizes; a reader
// that returns whole pages would lose where each packet ends. It stops at the end of the
// stream, at the first error, or when packet returns false.
func readOpus(r io.Reader, packet func([]byte) bool) error {
	var header [27]byte
	var p []byte // the packet being read, which may go on in the next page
	fail := func(err error) error {
		if errors.Is(err, io.EOF) { // the stream ended inside a page or a packet
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("reading an Ogg page: %w", err)
	}
	for n := 0; ; {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if errors.Is(err, io.EOF) && len(p) == 0 {
				if n < 3 { // a phrase with no audio would pass as heard
					return errors.New("no audio")
				}
				return nil
			}
			return fail(err)
		}
		if !bytes.Equal(header[:4], []byte("OggS")) {
			return errors.New("not an Ogg page")
		}
		sizes := make([]byte, header[26])
		if _, err := io.ReadFull(r, sizes); err != nil {
			return fail(err)
		}
		for _, size := range sizes {
			if len(p)+int(size) > maxPacketBytes {
				return fmt.Errorf("an Opus packet over %d bytes", maxPacketBytes)
			}
			p = append(p, make([]byte, size)...)
			if _, err := io.ReadFull(r, p[len(p)-int(size):]); err != nil {
				return fail(err)
			}
			if size == 255 {
				continue
			}
			switch {
			case n == 0 && !bytes.HasPrefix(p, []byte("OpusHead")), n == 1 && !bytes.HasPrefix(p, []byte("OpusTags")):
				return errors.New("not Opus")
			case n >= 2:
				if d := duration(p); d != speech.PacketDuration {
					return fmt.Errorf("an Opus packet of %v, want %v", d, speech.PacketDuration)
				}
				if !packet(p) {
					return nil
				}
			}
			n++
			p = nil
		}
	}
}

// duration returns the audio an Opus packet holds, from its first byte (RFC 6716, section 3.1):
// the configuration sets each frame's length, and the last two bits how many frames there are.
func duration(p []byte) time.Duration {
	if len(p) == 0 {
		return 0
	}
	config := p[0] >> 3
	var frame time.Duration
	switch {
	case config < 12: // SILK
		frame = []time.Duration{10, 20, 40, 60}[config%4] * time.Millisecond
	case config < 16: // hybrid
		frame = []time.Duration{10, 20}[config%2] * time.Millisecond
	default: // CELT
		frame = []time.Duration{2500, 5000, 10000, 20000}[config%4] * time.Microsecond
	}
	switch p[0] & 3 {
	case 0:
		return frame
	case 1, 2:
		return 2 * frame
	default:
		if len(p) < 2 {
			return 0
		}
		return time.Duration(p[1]&0x3f) * frame
	}
}
