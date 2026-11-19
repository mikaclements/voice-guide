package speech

import (
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// browserDelay is how long after a packet arrives the browser plays it: its
// jitter buffer, whose target was measured at Chrome's 20 ms floor, plus
// about 20 ms for its audio output. A round guess, to be measured once the
// page reports what it has played.
const browserDelay = 40 * time.Millisecond

// captureDelay is how long after the user says something it reaches voice, beside the network: the
// browser captures the microphone 10 ms at a time and Opus sends it 20 ms a packet, so the end of a
// packet's sound is about 30 ms old as the packet leaves. Those two figures added, measured once
// the page reports what it has played.
const captureDelay = 30 * time.Millisecond

// sent is one packet played: the phrase it speaks, its number in that phrase
// from 1, and when it went out.
type sent struct {
	phrase Phrase
	n      int
	at     time.Time
}

// heard returns how many bytes of the reply's text the user had heard when
// their word came, at cutIn, over a connection with the given round trip:
// every phrase whose packets had all gone out a round trip and the browser's
// delay before, then the share of the phrase playing whose packets had gone
// out, back to the end of its last whole word. A phrase whose length isn't
// known counts as not heard. It errs short: a word half heard isn't counted.
func heard(packets []sent, cutIn time.Time, roundTrip time.Duration) int {
	by := heardBy(cutIn, roundTrip)
	last := -1
	for i, p := range packets {
		if p.at.After(by) {
			break
		}
		last = i
	}
	if last < 0 {
		return 0
	}
	p := packets[last]
	if p.n >= p.phrase.Packets && p.phrase.Packets > 0 {
		return p.phrase.End
	}
	start := 0 // where the phrase playing starts: where the one before ended
	for _, q := range packets[:last] {
		if q.phrase.Index < p.phrase.Index {
			start = max(start, q.phrase.End)
		}
	}
	if p.phrase.Packets == 0 {
		return start
	}
	text := strings.TrimRightFunc(p.phrase.Text, unicode.IsSpace)
	k := len(text) * p.n / p.phrase.Packets
	cut := strings.LastIndexFunc(text[:k], unicode.IsSpace)
	if r, _ := utf8.DecodeRuneInString(text[k:]); unicode.IsSpace(r) {
		cut = k // the word before k ends at k
	}
	if words := strings.TrimRightFunc(text[:max(cut, 0)], unicode.IsSpace); words != "" {
		return max(start, p.phrase.End-len(text)+len(words))
	}
	return start
}

// heardBy is when the last packet the user had heard went out, as of their
// word reaching voice at at: a round trip and the browser's delay before.
func heardBy(at time.Time, roundTrip time.Duration) time.Time {
	return at.Add(-roundTrip - browserDelay)
}

// aired is what of the agent's replies has reached the user on a connection:
// the turn whose reply played last, when its first packet and its latest went
// out, and whether more of it may follow. Its turn's reply plays on its
// goroutine while the microphone's reads it.
type aired struct {
	mu            sync.Mutex
	run           *turnRun
	first, latest time.Time
	playing       bool
}

// note records that run's reply sent a packet at at, or stopped sending.
func (a *aired) note(run *turnRun, at time.Time, playing bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case playing && a.run != run:
		a.run, a.first, a.latest, a.playing = run, at, at, true
	case playing:
		a.latest = at
	case a.run == run:
		a.playing = false
	}
}

// due returns when the next packet would have gone out after the last one
// sent, or zero before any.
func (a *aired) due() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.latest.IsZero() {
		return time.Time{}
	}
	return a.latest.Add(PacketDuration)
}

// heard returns the turn whose reply's audio was reaching the user when they
// said what reached voice at at, or nil: by the same estimate as the part of a
// reply heard, its first packet had been heard by then, and it was still
// playing or its last packet was still being heard.
func (a *aired) heard(at time.Time, roundTrip time.Duration) *turnRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	by := heardBy(at, roundTrip)
	if a.run == nil || by.Before(a.first) || !a.playing && by.After(a.latest.Add(PacketDuration)) {
		return nil
	}
	return a.run
}
