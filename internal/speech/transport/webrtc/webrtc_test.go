package webrtc_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/mikaclements/voice-guide/internal/speech"
	webrtctransport "github.com/mikaclements/voice-guide/internal/speech/transport/webrtc"
)

var loopback = net.IPv4(127, 0, 0, 1)

// wait bounds how long a test waits for a connection to open or end: both
// take milliseconds on this machine's loopback, so 5s means it never will.
const wait = 5 * time.Second

// maxMessage is the transport's limit on a browser's message in these tests.
const maxMessage = 1024

func newTransport(t *testing.T) *webrtctransport.Transport {
	t.Helper()
	return newTransportLogging(t, slog.New(slog.DiscardHandler))
}

func newTransportLogging(t *testing.T, log *slog.Logger) *webrtctransport.Transport {
	t.Helper()
	tr, err := webrtctransport.New(webrtctransport.Addresses{IPs: []net.IP{loopback}}, maxMessage, log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

// newTransportOnPort returns a transport whose connections all share one UDP port on the
// loopback, written into answers as the addresses in announce, if any.
func newTransportOnPort(t *testing.T, port int, announce ...net.IP) *webrtctransport.Transport {
	t.Helper()
	tr, err := webrtctransport.New(webrtctransport.Addresses{IPs: []net.IP{loopback}, Port: port, Announce: announce}, maxMessage, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Registered first, so it runs after the connections' own cleanups, as voice closes it.
	t.Cleanup(tr.Close)
	return tr
}

// freeUDPPort is a UDP port on the loopback that nothing held a moment ago.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: loopback})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer func() { _ = c.Close() }()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// peer is the browser's side: it asks to receive audio, or sends its
// microphone too, and opens a data channel.
type peer struct {
	pc       *webrtc.PeerConnection
	dc       *webrtc.DataChannel
	opened   chan struct{}
	closed   chan struct{}
	messages chan string      // what the server sends on the data channel
	audio    chan *rtp.Packet // each audio packet the server sends
	mic      *webrtc.TrackLocalStaticRTP
}

// heard bounds what a peer keeps of what it receives: more than any test sends.
const heard = 64

func newPeer(t *testing.T, withAudio bool) *peer {
	t.Helper()
	media := &webrtc.MediaEngine{}
	if err := media.RegisterDefaultCodecs(); err != nil {
		t.Fatalf("RegisterDefaultCodecs: %v", err)
	}
	var settings webrtc.SettingEngine
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetIPFilter(func(ip net.IP) bool { return ip.Equal(loopback) })
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(media), webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("NewPeerConnection: %v", err)
	}
	// Graceful, so nothing of one test's peer still runs in the next.
	t.Cleanup(func() { _ = pc.GracefulClose() })
	p := &peer{pc: pc, opened: make(chan struct{}), closed: make(chan struct{}), messages: make(chan string, heard), audio: make(chan *rtp.Packet, heard)}
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			select {
			case p.audio <- packet:
			default:
			}
		}
	})
	var once sync.Once
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateClosed || s == webrtc.PeerConnectionStateFailed {
			once.Do(func() { close(p.closed) })
		}
	})
	if withAudio {
		if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			t.Fatalf("AddTransceiverFromKind: %v", err)
		}
	}
	dc, err := pc.CreateDataChannel("turns", nil)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}
	p.dc = dc
	dc.OnOpen(func() { close(p.opened) })
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		select {
		case p.messages <- string(m.Data):
		default:
		}
	})
	return p
}

func (p *peer) offer(t *testing.T) string {
	t.Helper()
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	gathered := webrtc.GatheringCompletePromise(p.pc)
	if err := p.pc.SetLocalDescription(offer); err != nil {
		t.Fatalf("SetLocalDescription: %v", err)
	}
	<-gathered
	return p.pc.LocalDescription().SDP
}

// connect answers the peer's offer and waits for its data channel to open.
func connect(t *testing.T, tr *webrtctransport.Transport, p *peer) speech.Connection {
	t.Helper()
	conn, _ := connectAnswered(t, tr, p)
	return conn
}

// connectAnswered connects as connect does, and returns the answer too.
func connectAnswered(t *testing.T, tr *webrtctransport.Transport, p *peer) (speech.Connection, string) {
	t.Helper()
	conn, answer, err := tr.Answer(context.Background(), p.offer(t))
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	t.Cleanup(func() { conn.Close(speech.Ended) })
	direction := "a=sendonly" // the agent's audio only
	if p.mic != nil {
		direction = "a=sendrecv" // and the microphone
	}
	for _, want := range []string{"m=audio", "opus/48000/2", direction, "m=application"} {
		if !strings.Contains(answer, want) {
			t.Errorf("the answer lacks %q: the audio tracks and the data channel", want)
		}
	}
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatalf("SetRemoteDescription: %v", err)
	}
	waitFor(t, p.opened, "the data channel to open")
	return conn, answer
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(wait):
		t.Fatalf("waited %s for %s", wait, what)
	}
}

func TestTheDataChannelCarriesMessagesBothWays(t *testing.T) {
	p := newPeer(t, true)
	conn := connect(t, newTransport(t), p)
	for _, m := range []string{`{"type":"turn","words":"Hi"}`, `{"type":"turn","words":"Again"}`} {
		if err := p.dc.SendText(m); err != nil {
			t.Fatalf("SendText: %v", err)
		}
	}
	for _, want := range []string{`{"type":"turn","words":"Hi"}`, `{"type":"turn","words":"Again"}`} {
		select {
		case got := <-conn.Messages():
			if string(got) != want {
				t.Errorf("Messages gave %s, want %s, in the order sent", got, want)
			}
		case <-time.After(wait):
			t.Fatalf("waited %s for the browser's message %s", wait, want)
		}
	}
	if err := conn.Send([]byte(`{"type":"text","text":"Hello"}`)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case got := <-p.messages:
		if want := `{"type":"text","text":"Hello"}`; got != want {
			t.Errorf("the browser got %s, want %s", got, want)
		}
	case <-time.After(wait):
		t.Fatalf("waited %s for the browser to get the message", wait)
	}
}

func TestAMessageOverTheLimitNeverReachesSpeech(t *testing.T) {
	p := newPeer(t, true)
	conn := connect(t, newTransport(t), p)
	// The answer states the limit, so a browser refuses to send more; one
	// that sends it anyway loses its data channel.
	_ = p.dc.SendText(strings.Repeat("x", maxMessage+1))
	select {
	case got := <-conn.Messages():
		t.Errorf("Messages gave %d bytes, over the %d-byte limit", len(got), maxMessage)
	case <-time.After(200 * time.Millisecond): // the loopback delivers in milliseconds
	}
}

func TestPlayedPacketsReachTheBrowserAsSentInOrder(t *testing.T) {
	p := newPeer(t, true)
	conn := connect(t, newTransport(t), p)
	sent := [][]byte{{0xf8, 0xff, 0xfe}, {0xf8, 0x01}, {0xf8, 0x02}}
	for _, packet := range sent {
		if err := conn.Play(packet, 0); err != nil {
			t.Fatalf("Play: %v", err)
		}
	}
	for i, want := range sent {
		select {
		case got := <-p.audio:
			if string(got.Payload) != string(want) {
				t.Errorf("packet %d reached the browser as %x, want %x", i, got.Payload, want)
			}
		case <-time.After(wait):
			t.Fatalf("waited %s for audio packet %d", wait, i)
		}
	}
}

func TestASilenceBeforeAPacketMovesItsTimestampNotItsSequenceNumber(t *testing.T) {
	p := newPeer(t, true)
	conn := connect(t, newTransport(t), p)
	// Two packets back to back, then one after 100 ms of silence.
	for _, silence := range []time.Duration{0, 0, 100 * time.Millisecond} {
		if err := conn.Play([]byte{0xf8, 0xff, 0xfe}, silence); err != nil {
			t.Fatalf("Play: %v", err)
		}
	}
	var got []*rtp.Packet
	for range 3 {
		select {
		case packet := <-p.audio:
			got = append(got, packet)
		case <-time.After(wait):
			t.Fatalf("waited %s for audio packet %d", wait, len(got))
		}
	}
	// Opus is stamped at 48 kHz, so 20 ms is 960 and 120 ms is 5760.
	if d := got[1].Timestamp - got[0].Timestamp; d != 960 {
		t.Errorf("timestamps of packets back to back differ by %d, want 960", d)
	}
	if d := got[2].Timestamp - got[1].Timestamp; d != 5760 {
		t.Errorf("timestamps across 100 ms of silence differ by %d, want 5760: the packet's 20 ms and the silence", d)
	}
	if got[1].SequenceNumber != got[0].SequenceNumber+1 || got[2].SequenceNumber != got[1].SequenceNumber+1 {
		t.Errorf("sequence numbers %d, %d, %d, want consecutive: no packet was lost", got[0].SequenceNumber, got[1].SequenceNumber, got[2].SequenceNumber)
	}
	if !got[0].Marker || got[1].Marker || !got[2].Marker {
		t.Errorf("markers %t, %t, %t, want set on the first packet after silence only", got[0].Marker, got[1].Marker, got[2].Marker)
	}
}

func TestSendFailsOnceTheConnectionHasEnded(t *testing.T) {
	p := newPeer(t, true)
	conn := connect(t, newTransport(t), p)
	conn.Close(speech.Ended)
	if err := conn.Send([]byte(`{"type":"text","text":"late"}`)); err == nil {
		t.Error("Send succeeded on a connection that has ended")
	}
	if err := conn.Play([]byte{0xf8, 0xff, 0xfe}, 0); err == nil {
		t.Error("Play succeeded on a connection that has ended")
	}
}

func TestClosingTellsTheBrowserWhyAndEndsTheConnectionForBothSides(t *testing.T) {
	p := newPeer(t, true)
	conn := connect(t, newTransport(t), p)
	// The browser's side of the channel opens before the server's; a message
	// the server receives shows its side is open too, so it can say why.
	if err := p.dc.SendText(`{"type":"turn","words":"Hi"}`); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	select {
	case <-conn.Messages():
	case <-time.After(wait):
		t.Fatalf("waited %s for the server's side of the data channel", wait)
	}
	closed := make(chan struct{})
	go func() { conn.Close(speech.Replaced); close(closed) }()
	waitFor(t, closed, "Close to return")
	waitFor(t, conn.Done(), "the connection to end")
	waitFor(t, p.closed, "the browser to see the connection end")
	// The server waits for the browser's acknowledgement, which comes from the
	// browser's network stack; its message handler can run a moment after the
	// connection reports closed, so the test waits for the message too.
	select {
	case m := <-p.messages:
		if want := `{"type":"closing","reason":"replaced"}`; m != want {
			t.Errorf("the browser got %s, want %s", m, want)
		}
	case <-time.After(wait):
		t.Error("the browser wasn't told why the connection ended")
	}
}

func TestAnAddressThisMachineLacksIsRefused(t *testing.T) {
	// 203.0.113.0/24 is reserved for documentation, so no machine has it.
	if _, err := webrtctransport.New(webrtctransport.Addresses{IPs: []net.IP{net.ParseIP("203.0.113.7")}}, maxMessage, slog.New(slog.DiscardHandler)); err == nil {
		t.Error("New accepted an address no interface has")
	}
}

func TestConnectionsSharingOnePortAreToldApart(t *testing.T) {
	port := freeUDPPort(t)
	tr := newTransportOnPort(t, port)
	first, second := newPeer(t, true), newPeer(t, true)
	conns := make([]speech.Connection, 2)
	for i, p := range []*peer{first, second} {
		var answer string
		conns[i], answer = connectAnswered(t, tr, p)
		if want := fmt.Sprintf("127.0.0.1 %d typ host", port); !strings.Contains(answer, want) {
			t.Errorf("connection %d's answer lacks the candidate %q, the shared port", i, want)
		}
	}
	// Each sends its own message; each connection must get its own browser's only.
	for i, p := range []*peer{first, second} {
		if err := p.dc.SendText(fmt.Sprintf("from browser %d", i)); err != nil {
			t.Fatalf("SendText: %v", err)
		}
	}
	for i, conn := range conns {
		select {
		case got := <-conn.Messages():
			if want := fmt.Sprintf("from browser %d", i); string(got) != want {
				t.Errorf("connection %d got %q, want %q", i, got, want)
			}
		case <-time.After(wait):
			t.Fatalf("waited %s for connection %d's message", wait, i)
		}
	}
}

func TestAnAnnouncedAddressTakesThePlaceOfTheMachinesOwnInTheAnswer(t *testing.T) {
	port := freeUDPPort(t)
	// 192.0.2.0/24 is reserved for documentation, so nothing answers there: the connection opens
	// because the server's checks reach the browser, which learns the real address from them, as a
	// browser behind a published port does from the packets the forwarder passes on.
	announced := net.ParseIP("192.0.2.10")
	_, answer := connectAnswered(t, newTransportOnPort(t, port, announced), newPeer(t, true))
	if want := fmt.Sprintf("192.0.2.10 %d typ host", port); !strings.Contains(answer, want) {
		t.Errorf("the answer lacks the candidate %q, the announced address on the shared port", want)
	}
	if strings.Contains(answer, "127.0.0.1") {
		t.Error("the answer still names the machine's own address")
	}
}

func TestAPortAlreadyTakenStopsTheStart(t *testing.T) {
	held, err := net.ListenUDP("udp4", &net.UDPAddr{IP: loopback})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer func() { _ = held.Close() }()
	port := held.LocalAddr().(*net.UDPAddr).Port
	_, err = webrtctransport.New(webrtctransport.Addresses{IPs: []net.IP{loopback}, Port: port}, maxMessage, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), fmt.Sprint(port)) {
		t.Errorf("New = %v, want an error naming port %d", err, port)
	}
}

func TestClosingTheTransportLetsGoOfItsPort(t *testing.T) {
	port := freeUDPPort(t)
	tr, err := webrtctransport.New(webrtctransport.Addresses{IPs: []net.IP{loopback}, Port: port}, maxMessage, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tr.Close()
	again, err := net.ListenUDP("udp4", &net.UDPAddr{IP: loopback, Port: port})
	if err != nil {
		t.Fatalf("port %d is still held once the transport closed: %v", port, err)
	}
	_ = again.Close()
}

func TestTheBrowserHangingUpEndsTheConnection(t *testing.T) {
	p := newPeer(t, true)
	conn := connect(t, newTransport(t), p)
	if err := p.pc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitFor(t, conn.Done(), "the connection to end once the browser hangs up")
}

func TestOffersThatCantBeAnsweredAreRefused(t *testing.T) {
	tr := newTransport(t)
	for name, offer := range map[string]string{
		"an unreadable offer":    "v=0\r\nnonsense",
		"an offer with no audio": newPeer(t, false).offer(t),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := tr.Answer(context.Background(), offer); !errors.Is(err, speech.ErrBadOffer) {
				t.Errorf("Answer = %v, want ErrBadOffer", err)
			}
		})
	}
}

func TestTheRoundTripToTheBrowserIsMeasuredOnceConnected(t *testing.T) {
	conn := connect(t, newTransport(t), newPeer(t, true))
	// The checks that connected the two measured it; on one machine it is well under a second.
	if rtt := conn.RoundTrip(); rtt <= 0 || rtt > time.Second {
		t.Errorf("RoundTrip = %s, want the measured round trip, above 0 and under a second", rtt)
	}
}
