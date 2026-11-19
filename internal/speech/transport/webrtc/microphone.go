package webrtc

import (
	"log/slog"
	"slices"
	"time"

	pionopus "github.com/pion/opus"
	"github.com/pion/rtp"

	"github.com/mikaclements/voice-guide/internal/speech"
)

// listenStep is the audio each stretch the transport yields holds: 20 ms,
// the packet browsers usually send. Packets of other lengths are cut or
// joined to it, so listenAhead stretches are a set time whatever the browser
// sends.
const listenStep = speech.ListenRate / 50

// listenAhead bounds the microphone audio decoded but not yet taken: 50
// stretches of 20 ms, a second. Speech gets through a second of audio in
// milliseconds, so it falls this far behind only when stalled, and then the
// newest audio is dropped rather than piled up: a round guess.
const listenAhead = 50

// maxMissing is speech.MaxMissing in samples.
const maxMissing = int(speech.MaxMissing * speech.ListenRate / time.Second)

// maxMisorder is how far behind the last packet taken one may be numbered
// and still be taken for late: 100, as RFC 3550 suggests. Further back, the
// browser may have started its numbering again, which two packets in order
// show.
const maxMisorder = 100

// opusClock is the rate RTP timestamps count Opus audio at, whatever rate it
// was made at (RFC 7587).
const opusClock = 48_000

// maxPacketSamples is the most samples one Opus packet decodes to at
// speech.ListenRate: a packet holds at most 120 ms of audio (RFC 6716).
const maxPacketSamples = speech.ListenRate * 120 / 1000

// microphone decodes the browser's microphone packets and yields their audio
// on audio in stretches of listenStep. It keeps the audio's time by the
// packets' numbers and timestamps: a duplicate, or a packet that comes too
// late, is dropped; audio that never came counts as missing, and so does audio
// speech was too slow to take. It logs each Opus mode the first time it comes,
// so what browsers send is known, and at the end what it dropped and why.
type microphone struct {
	audio   chan<- speech.UserAudio
	log     *slog.Logger
	decoder pionopus.Decoder
	pcm     []int16
	seen    map[string]bool

	started bool
	last    uint16  // the number of the last packet taken
	next    uint32  // the timestamp the next packet carries if none is lost
	pending []int16 // audio decoded, less than a stretch
	missing int     // audio missing before what is pending

	restarting bool   // a packet came numbered far behind the last taken
	restart    uint16 // its number

	packets, late, undecodable, lost, dropped int
}

func newMicrophone(audio chan<- speech.UserAudio, log *slog.Logger) (*microphone, error) {
	decoder, err := pionopus.NewDecoderWithOutput(speech.ListenRate, 1)
	if err != nil {
		return nil, err
	}
	return &microphone{audio: audio, log: log, decoder: decoder, pcm: make([]int16, maxPacketSamples), seen: map[string]bool{}}, nil
}

// take takes the next packet that arrived.
func (m *microphone) take(packet *rtp.Packet) {
	if len(packet.Payload) == 0 {
		return
	}
	// Numbered at or before the last taken: a duplicate, or a packet whose
	// time already counted as missing when the one after it came; unless it
	// is far behind and follows the one before it, when the numbering
	// started again (RFC 3550, appendix A.1).
	if behind := int16(packet.SequenceNumber - m.last); m.started && behind <= 0 {
		if behind > -maxMisorder || !m.renumbered(packet.SequenceNumber) {
			m.late++
			return
		}
	}
	m.restarting = false
	mode := mode(packet.Payload[0])
	if !m.seen[mode] {
		m.seen[mode] = true
		m.log.Info("microphone audio", "mode", mode, "configuration", packet.Payload[0]>>3)
	}
	n, err := m.decoder.DecodeToInt16(packet.Payload, m.pcm)
	if err != nil {
		if m.undecodable == 0 {
			m.log.Warn("decode the microphone", "mode", mode, "err", err)
		}
		m.undecodable++
		return // its time shows as a gap before the next
	}
	m.packets++
	if gap := int32(packet.Timestamp - m.next); m.started && gap > 0 {
		// What is pending goes as it is, so the gap falls where it was.
		if len(m.pending) > 0 {
			m.yield(m.pending)
			m.pending = nil
		}
		lost := int(gap) * speech.ListenRate / opusClock
		m.lost += lost
		m.missing = min(m.missing+lost, maxMissing)
	}
	m.started, m.last, m.next = true, packet.SequenceNumber, packet.Timestamp+uint32(n*opusClock/speech.ListenRate)
	m.pending = append(m.pending, m.pcm[:n]...)
	for len(m.pending) >= listenStep {
		m.yield(slices.Clone(m.pending[:listenStep]))
		m.pending = m.pending[listenStep:]
	}
}

// yield hands speech the audio and what was missing before it, or, when
// speech is too far behind to take it, drops it and counts it as missing.
func (m *microphone) yield(audio []int16) {
	select {
	case m.audio <- speech.UserAudio{Missing: m.missing, PCM: audio}:
		m.missing = 0
	default:
		m.dropped += len(audio)
		m.missing = min(m.missing+len(audio), maxMissing)
	}
}

// renumbered says whether a packet numbered seq, far behind the last taken,
// follows one that also was, so the browser has started its numbering again;
// if not, it remembers seq for the next.
func (m *microphone) renumbered(seq uint16) bool {
	if m.restarting && seq == m.restart+1 {
		return true
	}
	m.restarting, m.restart = true, seq
	return false
}

// end logs what the microphone dropped, once its track has ended.
func (m *microphone) end() {
	m.log.Info("microphone audio ended", "packets", m.packets, "late", m.late, "undecodable", m.undecodable,
		"lost_ms", m.lost*1000/speech.ListenRate, "dropped_ms", m.dropped*1000/speech.ListenRate)
}

// mode names how an Opus packet was coded, from its configuration number, the
// top five bits of its first byte (RFC 6716, section 3.1): SILK, made for
// speech; CELT, for music and low delay; or hybrid, the two at once.
func mode(toc byte) string {
	switch config := toc >> 3; {
	case config < 12:
		return "silk"
	case config < 16:
		return "hybrid"
	default:
		return "celt"
	}
}
