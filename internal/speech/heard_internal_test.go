package speech

import (
	"testing"
	"time"
)

// playedFrom lists the packets of each phrase in turn, one every
// PacketDuration from the clock's start, as play records them.
func playedFrom(phrases ...Phrase) []sent {
	var packets []sent
	for _, p := range phrases {
		for n := 1; n <= max(p.Packets, 10); n++ {
			packets = append(packets, sent{phrase: p, n: n, at: time.Time{}.Add(time.Duration(len(packets)) * PacketDuration)})
		}
	}
	return packets
}

func TestHowMuchOfAReplyWasHeardByWhenTheUserCutIn(t *testing.T) {
	// "Tell me about your team. What was the hardest call you made?"
	first := Phrase{Index: 0, Text: "Tell me about your team.", End: 24, Packets: 10}
	second := Phrase{Index: 1, Text: "What was the hardest call you made?", End: 60, Packets: 35}
	unknown := second
	unknown.Packets = 0
	accented := Phrase{Index: 1, Text: "Voilà tout.", End: 22, Packets: 12}
	packet := func(i int) time.Time { return time.Time{}.Add(time.Duration(i) * PacketDuration) }
	// The user's word arrives a round trip and the browser's delay after the
	// packet they last heard went out, which these cases leave out.
	at := func(i int) time.Time { return packet(i).Add(browserDelay) }
	for name, c := range map[string]struct {
		packets   []sent
		cutIn     time.Time
		roundTrip time.Duration
		want      int
	}{
		"before the first packet went out": {playedFrom(first, second), at(0).Add(-time.Millisecond), 0, 0},
		"part way into the first word":     {playedFrom(first, second), at(0), 0, 0},
		"the first phrase to its end":      {playedFrom(first, second), at(9), 0, 24},
		// Packet 19 is the second phrase's 10th of 35, so 10 of its 35 bytes: inside "the".
		"part way into a word of the second phrase": {playedFrom(first, second), at(19), 0, len("Tell me about your team. What was")},
		// The 12th of 35 reaches byte 12, the space after "the": that word was heard.
		"a word heard to its end":                  {playedFrom(first, second), at(21), 0, len("Tell me about your team. What was the")},
		"the second phrase's first word part said": {playedFrom(first, second), at(10), 0, 24},
		"a phrase whose length isn't known":        {playedFrom(first, unknown), at(30), 0, 24},
		"every packet":                             {playedFrom(first, second), at(100), 0, 60},
		// The word that came at packet 10 left the browser a round trip of two
		// packets after it heard packet 8, inside the first phrase's last word.
		"less the round trip": {playedFrom(first, second), at(10), 2 * PacketDuration, len("Tell me about your")},
		// An implementation that sends more packets than it said still counts the phrase as ended.
		"more packets than the phrase said": {playedFrom(Phrase{Index: 0, Text: "Hi there.", End: 9, Packets: 3}), at(5), 0, 9},
		// The 5th of 12 reaches byte 5, inside "à", whose second byte read alone is a no-break space.
		"inside a character": {playedFrom(Phrase{Index: 0, Text: "Hi there.", End: 9, Packets: 10}, accented), at(14), 0, len("Hi there.")},
	} {
		if got := heard(c.packets, c.cutIn, c.roundTrip); got != c.want {
			t.Errorf("%s: heard %d bytes, want %d", name, got, c.want)
		}
	}
}
