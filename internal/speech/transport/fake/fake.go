// Package fake sets up audio connections in memory: speech's fake transport,
// for the core's tests and for load tests.
package fake

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mikaclements/voice-guide/internal/speech"
)

// BadOffer is an offer the fake can't answer.
const BadOffer = "bad offer"

// held bounds the messages a fake connection keeps each way, far more than a
// test or a load test sends before reading them.
const held = 1024

// errEnded reports a message or a packet for a connection that has ended.
var errEnded = errors.New("the audio connection has ended")

// Transport answers every offer but BadOffer. It is safe for concurrent use.
type Transport struct {
	// Hold, when set while no Answer is under way, keeps each answer after it
	// waiting until it is closed or the answer's context ends, as a slow
	// network does.
	Hold chan struct{}
	// HoldClose, when set before a connection is answered, keeps each Close
	// of it waiting until it is closed, as a real connection waits for the
	// browser to take its closing message.
	HoldClose chan struct{}

	held     chan struct{}
	closing  chan struct{}
	mu       sync.Mutex
	answered int
	conns    []*Connection
}

// New returns a transport with no connections.
func New() *Transport {
	return &Transport{held: make(chan struct{}, 1), closing: make(chan struct{}, held)}
}

// WaitHeld returns once an answer waits on Hold.
func (t *Transport) WaitHeld() { <-t.held }

// WaitClosing returns once a Close waits on HoldClose.
func (t *Transport) WaitClosing() { <-t.closing }

// Answer sets up a connection, answering "answer to " and the offer.
func (t *Transport) Answer(ctx context.Context, offer string) (speech.Connection, string, error) {
	if t.Hold != nil {
		t.held <- struct{}{}
		select {
		case <-t.Hold:
		case <-ctx.Done():
			return nil, "", fmt.Errorf("gather addresses: %w", ctx.Err())
		}
	}
	if offer == BadOffer {
		return nil, "", fmt.Errorf("%w: unreadable", speech.ErrBadOffer)
	}
	c := &Connection{done: make(chan struct{}), messages: make(chan []byte, held), sent: make(chan string, held), audio: make(chan speech.UserAudio, held), playing: make(chan struct{}), holdClose: t.HoldClose, closing: t.closing}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.answered++
	t.conns = append(t.conns, c)
	return c, "answer to " + offer, nil
}

// Answered counts the offers answered.
func (t *Transport) Answered() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.answered
}

// Conn returns the i-th connection answered, from 0, to act as its browser.
func (t *Transport) Conn(i int) *Connection {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.conns[i]
}

// Open counts the connections not yet ended.
func (t *Transport) Open() int {
	n := 0
	for _, why := range t.Reasons() {
		if why == "" {
			n++
		}
	}
	return n
}

// Reasons lists, in the order they were answered, why each connection was
// closed here: empty for one still open, "browser" for one the browser ended.
func (t *Transport) Reasons() []speech.CloseReason {
	t.mu.Lock()
	defer t.mu.Unlock()
	reasons := make([]speech.CloseReason, len(t.conns))
	for i, c := range t.conns {
		c.mu.Lock()
		reasons[i] = c.why
		if c.why == "" && c.ended {
			reasons[i] = "browser"
		}
		c.mu.Unlock()
	}
	return reasons
}

// EndAll ends every connection from the browser's side, as hanging up does.
func (t *Transport) EndAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range t.conns {
		c.end("")
	}
}

// Connection is one connection, with the browser's side for tests: Say sends
// as the browser, Microphone sends its microphone's audio, Sent yields what
// reached it, and Played lists the agent's audio.
type Connection struct {
	mu       sync.Mutex
	why      speech.CloseReason
	ended    bool
	done     chan struct{}
	messages chan []byte
	sent     chan string
	played   []Packet
	playing  chan struct{} // closed, and replaced, as each packet plays
	audio    chan speech.UserAudio
	// holdClose and closing are the transport's HoldClose and its signal.
	holdClose chan struct{}
	closing   chan struct{}
	late      []string // messages sent once the connection had ended
}

// Late returns the messages speech tried to send once the connection had ended, in order.
func (c *Connection) Late() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.late...)
}

// Packet is an audio packet as played, when, and the silence before it.
type Packet struct {
	Opus    []byte
	At      time.Time
	Silence time.Duration
}

func (c *Connection) Done() <-chan struct{} { return c.done }

func (c *Connection) Close(why speech.CloseReason) {
	if c.holdClose != nil {
		c.closing <- struct{}{}
		<-c.holdClose
	}
	c.end(why)
}

func (c *Connection) Messages() <-chan []byte { return c.messages }

func (c *Connection) Send(msg []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		c.late = append(c.late, string(msg))
		return errEnded
	}
	select {
	case c.sent <- string(msg):
	default: // a browser no one reads, as in a load test, keeps the first ones
	}
	return nil
}

func (c *Connection) Play(opus []byte, silence time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return errEnded
	}
	if len(c.played) == held {
		c.played = c.played[1:] // a load test's long conversation keeps the latest
	}
	c.played = append(c.played, Packet{Opus: opus, At: time.Now(), Silence: silence})
	close(c.playing)
	c.playing = make(chan struct{})
	return nil
}

// RoundTrip is always 0: the fake browser is no distance away.
func (c *Connection) RoundTrip() time.Duration { return 0 }

func (c *Connection) Listen() <-chan speech.UserAudio { return c.audio }

// Say sends msg to the server as the browser does on its data channel.
func (c *Connection) Say(msg string) { c.messages <- []byte(msg) }

// Microphone sends a to the server as the browser's microphone track does,
// with what was missing before it. It waits while the server is behind by
// more than a test sends before reading.
func (c *Connection) Microphone(a speech.UserAudio) { c.audio <- a }

// Sent yields, in order, each message the server sent the browser.
func (c *Connection) Sent() <-chan string { return c.sent }

// Played lists the audio packets played so far.
func (c *Connection) Played() []Packet {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Packet(nil), c.played...)
}

// Watch lists the audio packets played so far, and returns a channel closed
// when the next one plays, so a test waits for audio instead of polling.
func (c *Connection) Watch() ([]Packet, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Packet(nil), c.played...), c.playing
}

// end ends the connection once, keeping the first reason.
func (c *Connection) end(why speech.CloseReason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	c.ended, c.why = true, why
	close(c.done)
}
