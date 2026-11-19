// Package webrtc sets up audio connections with browsers using Pion: speech's
// WebRTC transport. Each connection carries an Opus track for the agent's
// audio, the browser's microphone track when it sends one, and the data
// channel the browser opens.
package webrtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/mikaclements/voice-guide/internal/speech"
)

// Transport answers browsers' offers.
type Transport struct {
	api *webrtc.API
	log *slog.Logger
	// mux is the one UDP port every connection shares, when one is set.
	mux ice.UDPMux
}

// Addresses say where a transport takes browsers' audio.
type Addresses struct {
	// IPs are the addresses of this machine that take audio; none means its
	// private and loopback ones.
	IPs []net.IP
	// Port is one UDP port every connection shares, on each of those
	// addresses, so a firewall or a container publishes one port; ICE's
	// credentials tell the connections apart. 0 gives each connection ports
	// of its own, whichever the system hands out.
	Port int
	// Announce are written into answers in place of this machine's own
	// addresses: each in place of every address of its family, as for a
	// container whose port is published, or a server behind a one-to-one NAT.
	// None keeps the machine's own.
	Announce []net.IP
}

// New returns a transport that listens for browsers where at says, takes
// messages of up to maxMessage bytes on their data channels, and logs what it
// learns of each microphone track to log. It refuses an address no interface
// has: nothing would listen there, so every connection would be answered and
// then die at the ICE timeout; and a port it can't take, which nothing else
// would tell the browser. Close lets go of the port.
func New(at Addresses, maxMessage int, log *slog.Logger) (*Transport, error) {
	if len(at.IPs) > 0 {
		own, err := net.InterfaceAddrs()
		if err != nil {
			return nil, fmt.Errorf("list this machine's addresses: %w", err)
		}
		for _, ip := range at.IPs {
			if !slices.ContainsFunc(own, func(a net.Addr) bool { n, ok := a.(*net.IPNet); return ok && n.IP.Equal(ip) }) {
				return nil, fmt.Errorf("%s is not an address of this machine", ip)
			}
		}
	}
	media := &webrtc.MediaEngine{}
	// Opus only: the agent's audio is Opus already, and the browser always offers it.
	// 111 is the number browsers give Opus; an answer uses the offer's own number anyway.
	if err := media.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: opus, PayloadType: 111}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("register Opus: %w", err)
	}
	// Pion's defaults, which its own peer connections use: reports on what was
	// sent and received, which the browser's audio statistics rely on.
	interceptors := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(media, interceptors); err != nil {
		return nil, fmt.Errorf("register interceptors: %w", err)
	}
	var settings webrtc.SettingEngine
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetIPFilter(listenOn(at.IPs))
	// Stated in the answer, so a browser won't send more; without it a browser
	// could send messages of up to a gigabyte, all held in memory.
	settings.SetSCTPMaxMessageSize(uint32(maxMessage))
	if len(at.Announce) > 0 {
		external := make([]string, len(at.Announce))
		for i, ip := range at.Announce {
			external[i] = ip.String()
		}
		if err := settings.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{External: external, AsCandidateType: webrtc.ICECandidateTypeHost}); err != nil {
			return nil, fmt.Errorf("announce %v: %w", at.Announce, err)
		}
	}
	t := &Transport{log: log}
	if at.Port != 0 {
		// Bound now, on the same addresses as without it, so a port another
		// program holds stops the start.
		mux, err := ice.NewMultiUDPMuxFromPort(at.Port, ice.UDPMuxFromPortWithIPFilter(listenOn(at.IPs)), ice.UDPMuxFromPortWithLoopback())
		if err != nil {
			return nil, fmt.Errorf("take UDP port %d for audio: %w", at.Port, err)
		}
		settings.SetICEUDPMux(mux)
		t.mux = mux
	}
	t.api = webrtc.NewAPI(webrtc.WithMediaEngine(media), webrtc.WithInterceptorRegistry(interceptors), webrtc.WithSettingEngine(settings))
	return t, nil
}

// Close lets go of the shared UDP port, if there is one, once every
// connection has ended: speech ends them first.
func (t *Transport) Close() {
	if t.mux == nil {
		return
	}
	if err := t.mux.Close(); err != nil {
		t.log.Warn("let go of the audio port", "err", err)
	}
}

// listenOn says which of this machine's addresses take audio: the ones named,
// or by default the private and loopback ones, so a laptop's public IPv6
// address doesn't open audio ports to the internet; a server names its own.
func listenOn(ips []net.IP) func(net.IP) bool {
	if len(ips) == 0 {
		return func(ip net.IP) bool { return ip.IsPrivate() || ip.IsLoopback() }
	}
	return func(ip net.IP) bool { return slices.ContainsFunc(ips, ip.Equal) }
}

// opus is the agent's audio: 48 kHz as RTP always describes Opus, two
// channels as its SDP name requires though the agent speaks in one, and the
// parameters browsers offer (10 ms packets allowed, loss recovery in-band).
var opus = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1"}

// Answer answers the offer once this machine's addresses are gathered, since
// the answer carries them all: with no relay or address-discovery server to
// ask, that takes no network round trip.
func (t *Transport) Answer(ctx context.Context, offer string) (speech.Connection, string, error) {
	desc := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}
	parsed, err := desc.Unmarshal()
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", speech.ErrBadOffer, err)
	}
	hasAudio := false
	for _, m := range parsed.MediaDescriptions {
		hasAudio = hasAudio || m.MediaName.Media == "audio"
	}
	if !hasAudio {
		return nil, "", fmt.Errorf("%w: it has no audio", speech.ErrBadOffer)
	}
	pc, err := t.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", fmt.Errorf("new peer connection: %w", err)
	}
	c := &connection{
		pc: pc, ended: make(chan struct{}, 1), closing: make(chan struct{}), stopping: make(chan struct{}), done: make(chan struct{}),
		messages: make(chan []byte, messagesAhead), audio: make(chan speech.UserAudio, listenAhead), log: t.log,
		// Random, as RTP asks, so packets can't be guessed from the connection's start.
		seq: uint16(rand.Uint32()), timestamp: rand.Uint32(),
	}
	answer, err := c.answer(ctx, desc)
	if err != nil {
		c.stop()
		_ = pc.GracefulClose()
		c.readers.Wait()
		return nil, "", err
	}
	go c.run()
	return c, answer, nil
}

// connection is one browser's peer connection. Its run goroutine ends it,
// once the browser hangs up or it fails or Close is called.
type connection struct {
	pc        *webrtc.PeerConnection
	ended     chan struct{} // the connection ended on its own; room for one signal
	closing   chan struct{}
	closeOnce sync.Once
	why       speech.CloseReason // set before closing is closed
	stopping  chan struct{}      // closed once the connection starts to end
	done      chan struct{}
	readers   sync.WaitGroup
	messages  chan []byte
	track     *webrtc.TrackLocalStaticRTP
	audio     chan speech.UserAudio
	log       *slog.Logger

	// The next audio packet's sequence number and timestamp, and whether one
	// has gone out; Play stamps each packet itself, as only it knows of silence.
	playing   sync.Mutex
	seq       uint16
	timestamp uint32
	started   bool

	mu        sync.Mutex
	channel   *webrtc.DataChannel // the first one the browser opens
	listening bool                // a microphone track is being read
}

// messagesAhead bounds the browser's messages read but not yet taken. A
// browser sends one message a turn, so 16 is room to spare; a browser that
// sends faster than speech reads waits, as its own data channel buffers.
const messagesAhead = 16

// errEnded reports a message or a packet for a connection that has ended or
// is ending.
var errEnded = errors.New("the audio connection has ended")

// errNoChannel reports a message for a browser that hasn't opened its data
// channel yet.
var errNoChannel = errors.New("the browser hasn't opened a data channel")

// closingMessage tells the browser on its data channel why the server ends
// the connection: {"type":"closing","reason":"replaced"}, say.
type closingMessage struct {
	Type   string             `json:"type"`
	Reason speech.CloseReason `json:"reason"`
}

// ackWait bounds the wait for the browser to acknowledge the closing message,
// so the close doesn't lose it. A browser acknowledges within a round trip,
// milliseconds nearby and well under half a second across the world, so a
// second only passes when the browser has gone quiet.
const ackWait = time.Second

func (c *connection) answer(ctx context.Context, offer webrtc.SessionDescription) (string, error) {
	c.pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.channel != nil {
			return // a second channel is ignored: what it carries is dropped
		}
		c.channel = dc
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			// Waits while speech is behind, which holds the browser back; not
			// once the connection ends, when no one reads any more.
			select {
			case c.messages <- m.Data:
			case <-c.stopping:
			}
		})
	})
	c.pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		// Started under the lock stop closes stopping under, so no reader
		// starts once run may be waiting for the readers to finish.
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.listening || c.isStopping() {
			return // one microphone: another track's packets go unread
		}
		c.listening = true
		c.readers.Go(func() { c.listen(track) })
	})
	c.pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		// Disconnected can recover; failed is about 30s without traffic, under
		// Pion's ICE timeouts; closed is the browser hanging up.
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			select {
			case c.ended <- struct{}{}:
			default:
			}
		}
	})
	if err := c.pc.SetRemoteDescription(offer); err != nil {
		return "", fmt.Errorf("%w: %v", speech.ErrBadOffer, err)
	}
	track, err := webrtc.NewTrackLocalStaticRTP(opus, "audio", "agent")
	if err != nil {
		return "", fmt.Errorf("new track: %w", err)
	}
	sender, err := c.pc.AddTrack(track)
	if err != nil {
		return "", fmt.Errorf("add the track: %w", err)
	}
	c.track = track
	c.readers.Go(func() {
		// The browser's reports must be read for Pion to act on them; reading
		// stops when the connection closes.
		buf := make([]byte, 1500) // one packet at the usual network packet size
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	})
	answer, err := c.pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", speech.ErrBadOffer, err)
	}
	gathered := webrtc.GatheringCompletePromise(c.pc)
	if err := c.pc.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("set the answer: %w", err)
	}
	select {
	case <-gathered:
	case <-ctx.Done():
		return "", fmt.Errorf("gather addresses: %w", ctx.Err())
	}
	return c.pc.LocalDescription().SDP, nil
}

func (c *connection) run() {
	select {
	case <-c.ended:
		c.stop()
	case <-c.closing:
		c.stop()
		c.tell(c.why)
	}
	// Graceful: it returns once Pion's own goroutines for the connection have.
	_ = c.pc.GracefulClose()
	c.readers.Wait()
	close(c.done)
}

// tell sends the browser why the connection ends, if it opened a data
// channel, and waits for the browser to acknowledge it, so closing doesn't
// drop it. A connection closed before the server's side of the channel has
// opened, within moments of connecting, can't say why: the browser sees an
// ordinary close.
func (c *connection) tell(why speech.CloseReason) {
	c.mu.Lock()
	dc := c.channel
	c.mu.Unlock()
	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return
	}
	acked := make(chan struct{})
	var once sync.Once
	// Pion counts a message as buffered until the browser acknowledges it.
	dc.SetBufferedAmountLowThreshold(0)
	dc.OnBufferedAmountLow(func() { once.Do(func() { close(acked) }) })
	msg, err := json.Marshal(closingMessage{Type: "closing", Reason: why})
	if err != nil || dc.SendText(string(msg)) != nil {
		return
	}
	select {
	case <-acked:
	case <-time.After(ackWait):
	}
}

func (c *connection) Done() <-chan struct{} { return c.done }

func (c *connection) Messages() <-chan []byte { return c.messages }

func (c *connection) Listen() <-chan speech.UserAudio { return c.audio }

// stop marks the connection as ending, once.
func (c *connection) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	close(c.stopping)
}

// listen hears the browser's microphone track until it ends, as it does
// when the connection closes.
func (c *connection) listen(track *webrtc.TrackRemote) {
	mic, err := newMicrophone(c.audio, c.log)
	if err != nil {
		c.log.Error("start decoding the microphone", "err", err)
		return
	}
	for {
		packet, _, err := track.ReadRTP()
		if err != nil {
			break
		}
		mic.take(packet)
	}
	mic.end()
}

func (c *connection) Send(msg []byte) error {
	if c.isStopping() {
		return errEnded
	}
	c.mu.Lock()
	dc := c.channel
	c.mu.Unlock()
	if dc == nil {
		return errNoChannel
	}
	return dc.SendText(string(msg))
}

// Play stamps each packet as RTP asks (RFC 3550): the sequence number counts
// packets sent, so a silence isn't taken for lost packets, and the timestamp
// counts audio time, silence included, so the browser doesn't take a silence
// for network delay and grow its buffer. The first packet after a silence is
// marked, as audio over RTP does at the start of a talkspurt (RFC 3551).
func (c *connection) Play(opus []byte, silence time.Duration) error {
	if c.isStopping() {
		return errEnded
	}
	c.playing.Lock()
	defer c.playing.Unlock()
	c.timestamp += ticks(silence)
	packet := &rtp.Packet{Header: rtp.Header{Version: 2, Marker: !c.started || silence > 0, SequenceNumber: c.seq, Timestamp: c.timestamp}, Payload: opus}
	c.seq, c.timestamp, c.started = c.seq+1, c.timestamp+ticks(speech.PacketDuration), true
	return c.track.WriteRTP(packet)
}

// ticks is d in Opus's RTP clock, which always runs at 48 kHz.
func ticks(d time.Duration) uint32 { return uint32(d * 48_000 / time.Second) }

// RoundTrip is the latest round trip on the pair of addresses in use, measured by the checks
// ICE repeats every few seconds to confirm the browser still wants the audio (RFC 7675).
func (c *connection) RoundTrip() time.Duration {
	sctp := c.pc.SCTP()
	if sctp == nil {
		return 0
	}
	stats, ok := sctp.Transport().ICETransport().GetSelectedCandidatePairStats()
	if !ok {
		return 0
	}
	return time.Duration(stats.CurrentRoundTripTime * float64(time.Second))
}

func (c *connection) isStopping() bool {
	select {
	case <-c.stopping:
		return true
	default:
		return false
	}
}

func (c *connection) Close(why speech.CloseReason) {
	c.closeOnce.Do(func() {
		c.why = why
		close(c.closing)
	})
	<-c.done
}
