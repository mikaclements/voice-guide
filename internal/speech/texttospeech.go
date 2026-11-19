package speech

import (
	"context"
	"time"
)

// PacketDuration is the audio in every packet text-to-speech yields: 20 ms,
// WebRTC's usual Opus packet, so packets go out on the audio track as they are
// and are paced by counting them.
const PacketDuration = 20 * time.Millisecond

// TextToSpeech turns an agent's reply into speech while the reply is still
// arriving.
type TextToSpeech interface {
	// Speak reads the reply's text as the model streams it, until text is
	// closed, and sends its speech in voice at speed, as the
	// implementation names its voices and 1 its voice's own pace, on the returned channel: Opus packets of PacketDuration each, in
	// the order they play. It keeps reading text while it speaks, and after a
	// failure, so speaking never holds the reply's text back. How much text it
	// waits for before speaking is the implementation's choice. The channel is
	// closed after the last packet, or soon after cancelling ctx stops the
	// speech; a failure ends it with one Packet carrying Err.
	Speak(ctx context.Context, voice string, speed float64, text <-chan string) <-chan Packet
}

// Packet is one packet of a reply's speech, or the failure that ended it.
type Packet struct {
	// Opus is one Opus packet of PacketDuration.
	Opus []byte
	// Phrase is the part of the reply this packet speaks, so a user
	// stopped after it has heard the phrases before it and some of this one.
	Phrase Phrase
	Err    error
}

// Phrase is a part of a reply spoken in one go, numbered from 0 in the reply.
type Phrase struct {
	Index int
	Text  string
	// End is where the phrase ends in the reply's text, in bytes: a user
	// who heard this phrase has heard the reply up to End.
	End int
	// Ready is when the phrase's text was complete, so the first phrase's
	// Ready is when the reply first had something to speak.
	Ready time.Time
	// Packets is how many packets the phrase has, 0 when the implementation
	// doesn't know by its first, so speech can tell how much of it a user who
	// cut in had heard.
	Packets int
}
