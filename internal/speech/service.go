package speech

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/conversation"
)

// Transport sets up audio connections with browsers: the swap point for how
// audio travels.
type Transport interface {
	// Answer sets up a connection from a browser's offer and returns it with
	// the answer to send back, or an error wrapping ErrBadOffer for an offer it
	// can't answer.
	Answer(ctx context.Context, offer string) (Connection, string, error)
}

// ListenRate is the sample rate of the user's audio as a connection yields
// it: 16 kHz, the rate speech detection and the end-of-turn check take, and
// enough for speech, whose sound lies under 8 kHz.
const ListenRate = 16_000

// Connection is one browser's audio connection: the agent's audio track, the
// user's microphone track when the browser sends one, and the data channel
// the browser opened, which carries typed turns, the reply's text and what
// speech hears the user say.
type Connection interface {
	// Done is closed once the connection has ended: closed here, hung up by
	// the browser, or lost.
	Done() <-chan struct{}
	// Close tells the browser why the connection ends, ends it, and returns
	// once it has ended.
	Close(why CloseReason)
	// Messages yields what the browser sends on its data channel, in order.
	// It is never closed: read it until Done.
	Messages() <-chan []byte
	// Send sends the browser one message on its data channel. It fails before
	// the browser has opened one and once the connection has ended.
	Send(msg []byte) error
	// Play sends one Opus packet of PacketDuration on the audio track, to be
	// heard as it arrives: the caller paces the packets. silence is how long
	// nothing played before it, beyond the packets before it, so its timestamp
	// keeps to real time and the browser doesn't take the gap for delay.
	Play(opus []byte, silence time.Duration) error
	// RoundTrip is the latest round trip measured to the browser, 0 before
	// the first: how far behind what was sent the browser's word arrives.
	RoundTrip() time.Duration
	// Listen yields the user's microphone audio as it arrives, in order.
	// Audio the reader is too slow to take is dropped, not held, so a
	// stalled reader costs no memory, and counted as missing, as is audio
	// that never arrived, so time is kept. A browser that sends no
	// microphone sends nothing. It is never closed: read it until Done.
	Listen() <-chan UserAudio
}

// UserAudio is the next stretch of the user's microphone audio: Missing
// samples that never arrived or were dropped, at most MaxMissing, then PCM,
// mono 16-bit at ListenRate. What is missing counts as time, so a pause lasts as long as it
// did even when packets were lost on the way.
type UserAudio struct {
	Missing int
	PCM     []int16
}

// MaxMissing is the most audio a connection counts as missing before any one
// stretch: 10 s. A browser's timestamps can jump, as when it starts its audio
// again, and a jump mustn't count as hours of silence; the waits that silence
// ends are shorter.
const MaxMissing = 10 * time.Second

// CloseReason tells the browser why this server ends its connection, so it
// knows whether to open another.
type CloseReason string

const (
	// Ended: the caller ended it, its session ended, or it was refused
	// before the browser had it.
	Ended CloseReason = "ended"
	// Replaced: a newer connection took the session; the page doesn't
	// reconnect, or two tabs would keep taking the session from each other.
	Replaced CloseReason = "replaced"
	// Stopping: the server stops; the page reconnects, to any server.
	Stopping CloseReason = "stopping"
	// Expired: the ticket that opened it has passed its lifetime; the page
	// doesn't reconnect, and says the session has expired.
	Expired CloseReason = "expired"
)

// MessageLimit is the largest message a browser may send on its data channel
// for turns of up to chars characters of words and screen input: a character
// takes at most 12 bytes escaped in JSON, plus room for the rest of the turn.
func MessageLimit(chars int) int { return 12*chars + 64<<10 }

// ErrBadOffer reports an offer that can't be answered.
var ErrBadOffer = errors.New("the offer can't be answered")

// ErrNoConnection reports a connection this server doesn't hold: ended,
// replaced, or held by another server.
var ErrNoConnection = errors.New("no such audio connection on this server")

// ErrTicketExpired reports a connection asked for with a ticket that has
// expired by this server's clock, which the credential check, allowing for
// clocks apart, let through.
var ErrTicketExpired = errors.New("the ticket has expired")

// ErrStopping reports a connection asked for while the server stops.
var ErrStopping = errors.New("the server is stopping")

// ErrFull reports a server already holding as many connections as it may.
var ErrFull = errors.New("this server holds as many audio connections as it may")

// Service holds the audio connections this server answered, at most one per
// session and at most a set number in all. A connection lives on the server
// that answered it; the session it belongs to stays in the database, so a
// browser that loses its connection opens a new one with any server.
type Service struct {
	conversation *conversation.Service
	transport    Transport
	tts          TextToSpeech
	detection    SpeechDetection
	stt          SpeechToText
	endOfTurn    EndOfTurn
	settings     Settings
	log          *slog.Logger
	clock        clock

	mu        sync.Mutex
	conns     map[uuid.UUID]liveConn // by session
	answering map[uuid.UUID]int      // offers being answered, by session
	stopping  bool
	watchers  sync.WaitGroup // one per connection held, until it ends, and one per replaced one closing
}

// Settings are speech's settings.
type Settings struct {
	// MaxConnections bounds the audio connections this server holds.
	MaxConnections int
	// Voice is the voice replies are spoken in, as text-to-speech names it.
	Voice string
	// TextToSpeech names what speaks, for the turn's stage times.
	TextToSpeech string
	// Listening sets how the user is noticed speaking, pausing and cutting in.
	Listening Listening
}

// liveConn is an audio connection this server holds, with what ends it.
type liveConn struct {
	id   uuid.UUID
	conn Connection
	// hangup, closed once, asks the connection's turns to end it as its caller asked.
	hangup     chan struct{}
	hangupOnce *sync.Once
	// served is closed once the connection has ended, its turns are saved and the server has let
	// it go, so its place is free by the time a hang-up waiting on served returns.
	served chan struct{}
}

// NewService returns the service, checking sessions and taking turns with
// conversation, setting up connections with the transport, speaking replies
// with text-to-speech, noticing the user speak with speech detection, hearing
// their words with speech-to-text, and asking the end-of-turn check whether
// they have finished.
func NewService(c *conversation.Service, t Transport, tts TextToSpeech, d SpeechDetection, stt SpeechToText, eot EndOfTurn, s Settings, log *slog.Logger) *Service {
	return &Service{conversation: c, transport: t, tts: tts, detection: d, stt: stt, endOfTurn: eot, settings: s, log: log, clock: systemClock{}, conns: make(map[uuid.UUID]liveConn), answering: make(map[uuid.UUID]int)}
}

// Connect answers the browser's offer for one of the caller's sessions and
// returns the new connection's ID with the answer. A newer connection
// replaces the session's old one on this server, the way reloading the page
// reconnects; the old one is told it was replaced. One held by another server
// ends when its browser closes it.
func (s *Service) Connect(ctx context.Context, c access.Caller, session uuid.UUID, offer string) (uuid.UUID, string, error) {
	// A connection would end at once, telling the page nothing it could act on.
	if c.ExpiredAt(s.clock.Now()) {
		return uuid.Nil(), "", ErrTicketExpired
	}
	// Checked before any place is kept, so an offer for a session out of the
	// caller's reach never holds one while the lookup runs. An ended session
	// takes no more turns, so it gets no audio to take them on.
	if found, err := s.conversation.Session(ctx, c, session); err != nil {
		return uuid.Nil(), "", err
	} else if found.Ended() {
		return uuid.Nil(), "", conversation.ErrSessionEnded
	}
	if err := s.reserve(session); err != nil {
		return uuid.Nil(), "", err
	}
	// Deferred, so a panic can't keep the place for good; once the connection
	// is held, the session keeps its place through it.
	defer s.release(session)
	conn, answer, err := s.transport.Answer(ctx, offer)
	if err != nil {
		return uuid.Nil(), "", err
	}
	id := uuid.NewV7()
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		conn.Close(Ended)
		return uuid.Nil(), "", ErrStopping
	}
	old, replacing := s.conns[session]
	lc := liveConn{id: id, conn: conn, hangup: make(chan struct{}), hangupOnce: &sync.Once{}, served: make(chan struct{})}
	s.conns[session] = lc
	s.watchers.Add(1)
	if replacing {
		s.watchers.Add(1)
	}
	s.mu.Unlock()
	go s.serve(c, session, id, conn, lc.hangup, lc.served)
	if replacing {
		// Not waited for here: the old browser's acknowledgement shouldn't hold up the new one.
		go func() {
			defer s.watchers.Done()
			old.conn.Close(Replaced)
		}()
	}
	return id, answer, nil
}

// offersPerSession bounds a session's offers being answered at once: a reload
// racing a second tab makes two. Each builds a WebRTC connection, with its
// sockets and goroutines, before the limit on connections held can count it,
// so more are refused as full.
const offersPerSession = 2

// reserve counts the offer as being answered, keeping its session's place, so
// offers arriving together can't all be answered past the limit only to be
// closed: not while stopping, not beyond the session's few offers at once, and
// not past the limit for a session without a place. A session holds one place
// while it has a connection here or offers being answered, so a reload or a
// second tab never takes a second place, nor loses the session's own when the
// connection it replaces ends first.
func (s *Service) reserve(session uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return ErrStopping
	}
	if s.answering[session] >= offersPerSession {
		return ErrFull
	}
	if !s.placed(session) && s.places() >= s.settings.MaxConnections {
		return ErrFull
	}
	s.answering[session]++
	return nil
}

// release forgets the offer once it is held or has failed.
func (s *Service) release(session uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answering[session]--
	if s.answering[session] == 0 {
		delete(s.answering, session)
	}
}

// placed says whether the session holds a place. The caller holds mu.
func (s *Service) placed(session uuid.UUID) bool {
	_, connected := s.conns[session]
	_, answering := s.answering[session]
	return connected || answering
}

// places counts the sessions holding a place. The caller holds mu.
func (s *Service) places() int {
	n := len(s.conns)
	for session := range s.answering {
		if _, connected := s.conns[session]; !connected {
			n++
		}
	}
	return n
}

// letGo forgets the connection once it has ended, unless a newer one has
// replaced it.
func (s *Service) letGo(session, id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns[session].id == id {
		delete(s.conns, session)
	}
}

// Hangup ends one of the caller's audio connections, if this server still
// holds it: a connection since replaced or ended is left alone, so a closing
// tab's late request can't end its successor. A reply reaching the user is
// cut where they stopped hearing it, as an interrupt is. It returns once the
// connection has ended and its turns are saved, the cut included, so an end
// the caller sends next lands after them.
func (s *Service) Hangup(ctx context.Context, c access.Caller, session, id uuid.UUID) error {
	if _, err := s.conversation.Session(ctx, c, session); err != nil {
		return err
	}
	s.mu.Lock()
	lc, ok := s.conns[session]
	s.mu.Unlock()
	if !ok || lc.id != id || ended(lc.conn) {
		return ErrNoConnection
	}
	lc.hangupOnce.Do(func() { close(lc.hangup) })
	select {
	case <-lc.served:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// Close ends every connection, all at once, and returns once each has ended;
// connections asked for afterwards are refused.
func (s *Service) Close() {
	s.mu.Lock()
	s.stopping = true
	all := make([]liveConn, 0, len(s.conns))
	for _, lc := range s.conns {
		all = append(all, lc)
	}
	s.mu.Unlock()
	var closing sync.WaitGroup
	for _, lc := range all {
		closing.Go(func() { lc.conn.Close(Stopping) })
	}
	closing.Wait()
	s.watchers.Wait()
}

func ended(c Connection) bool {
	select {
	case <-c.Done():
		return true
	default:
		return false
	}
}
