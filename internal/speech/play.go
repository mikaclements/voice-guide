package speech

import (
	"context"
	"time"
)

// play sends each packet on the audio track in real time, one every
// PacketDuration, counted from the first: sent faster, packets would pile up
// in the browser and the reply would end before it was heard. Once ctx ends,
// as when the user cuts in, or a packet won't go, it stops playing and reads
// the rest, so text-to-speech can finish. It records each packet it sent, and
// tells onAir when each went out and when it stopped sending. due is when the
// reply before's next packet would have gone out, zero before any: the pause
// since then is a silence like any other, so the audio keeps to real time
// between replies too; uncounted, the browser took the next reply as arriving
// seconds late and now and then held its first sound back for about a second.
func play(ctx context.Context, conn Connection, speech <-chan Packet, clock clock, due time.Time, onAir func(at time.Time, playing bool)) played {
	var p played
	defer func() {
		if p.stopped.IsZero() {
			onAir(clock.Now(), false)
		}
	}()
	next := due
	stopped := false
	n := 0 // the packet's number in its phrase
	for packet := range speech {
		switch {
		case stopped:
		case packet.Err != nil:
			p.err = packet.Err
		default:
			// A gap in the speech, as between phrases made one after the
			// other, starts the count again: lost time isn't made up in a
			// burst, and the transport is told how long the silence was.
			var silence time.Duration
			if now := clock.Now(); now.Sub(next) > PacketDuration {
				if !next.IsZero() {
					silence = now.Sub(next)
				}
				next = now
			}
			// ctx is checked again after the wait, which may return with both
			// ready: no packet goes out once the user has cut in.
			stopped = !clock.Until(ctx, next) || ctx.Err() != nil || conn.Play(packet.Opus, silence) != nil
			if stopped {
				p.stopped = clock.Now()
				onAir(p.stopped, false)
				continue
			}
			if p.first.IsZero() {
				p.first, p.phraseReady = clock.Now(), packet.Phrase.Ready
			}
			if len(p.sent) == 0 || p.sent[len(p.sent)-1].phrase.Index != packet.Phrase.Index {
				n = 0
			}
			n++
			p.sent = append(p.sent, sent{phrase: packet.Phrase, n: n, at: clock.Now()})
			onAir(clock.Now(), true)
			next = next.Add(PacketDuration)
		}
	}
	return p
}

// played is what playing a reply came to: when its first packet went out,
// when the text of that packet's phrase was complete, every packet sent, when
// playing stopped early, if it did, and the failure that ended the speech, if
// any.
type played struct {
	first, phraseReady time.Time
	sent               []sent
	stopped            time.Time
	err                error
}
