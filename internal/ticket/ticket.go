// Package ticket makes and checks what voice-guide signs for the user's
// browser: a ticket for one session, which a product's server hands it. It is
// the only package that knows its format.
package ticket

import (
	"errors"
	"fmt"
	"log/slog"
	"time"
	"uuid"

	"github.com/golang-jwt/jwt/v5"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/secret"
)

// minSecret matches the size of the HS256 hash; a shorter secret is easier to guess.
const minSecret = 32

// leeway absorbs small clock differences between the machine that mints a
// ticket and the one that checks it, such as a container's clock lagging its host.
// Clocks kept by NTP agree within milliseconds; 5s is a wide margin that still
// leaves a ticket's lifetime of hours meaningful.
const leeway = 5 * time.Second

// Lifetime caps how long a session's tickets work, counted from when the
// session started, however long it stays live, so a stolen ticket stops at
// the latest then. A round guess: a practice session runs well under an hour;
// to be measured once products use tickets.
const Lifetime = 4 * time.Hour

// EndGrace is how long past its lifetime a ticket still ends its session, as
// the page does once it learns its ticket ran out, or a user pressing End
// late. The idle sweep ends a session nobody touches within five minutes, so
// a quarter of an hour leaves room for one pressed late; a round guess, to be
// measured once products use tickets.
const EndGrace = 15 * time.Minute

// ErrExpired reports a ticket voice-guide signed that has passed its
// lifetime: its holder needs a new session, not a fix.
var ErrExpired = errors.New("the ticket has expired")

// Key signs and verifies tickets.
type Key struct {
	secret []byte
}

// String and LogValue keep the secret out of logs and formatted output.
func (Key) String() string { return "ticket.Key(redacted)" }

// LogValue implements slog.LogValuer.
func (k Key) LogValue() slog.Value { return slog.StringValue(k.String()) }

// NewKey returns a key for the secret, refusing one too short to be safe.
func NewKey(secret []byte) (Key, error) {
	if len(secret) < minSecret {
		return Key{}, fmt.Errorf("signing key: %d bytes, need at least %d", len(secret), minSecret)
	}
	return Key{secret: secret}, nil
}

// Ticket is a ticket itself, as the product's server hands it to the user's
// browser. It formats, logs and encodes redacted, so a ticket passed around
// never reaches a log or an error; Reveal gives the ticket, to send it. The
// ticket sits behind a pointer, so even printing a struct that holds a Ticket
// where its methods can't be called shows an address, not the ticket.
type Ticket struct {
	heldTicket
}

// heldTicket is the ticket a Ticket holds, embedded under a name no other
// package can reach, so only MintTicket makes a Ticket that holds one.
type heldTicket = secret.Value[Ticket]

// claims are what a ticket names. A claim not named here is ignored, so a
// ticket minted by a version that wrote one more still works on one that no
// longer reads it, until it expires.
type claims struct {
	Workspace string `json:"ws"`
	// Session is the one session a ticket works in.
	Session string `json:"sid"`
	jwt.RegisteredClaims
}

// MintTicket returns a ticket for the caller's session, which started at
// started, issued now: it names the session's workspace and user, and works
// only in that session, until Lifetime after the session started,
// however late it is minted, so a repeat of the start never stretches the
// session's tickets. A clock that reads the start a little after now caps it
// at Lifetime from now instead. Once the session's tickets have expired it
// mints none: ErrExpired.
func (k Key) MintTicket(c access.Caller, started, now time.Time) (Ticket, error) {
	if c.Workspace == uuid.Nil() || c.User == uuid.Nil() || c.Session == uuid.Nil() || c.Product {
		return Ticket{}, errors.New("mint ticket: a ticket names a workspace, a user and a session, and no product")
	}
	expires := earlier(started, now).Add(Lifetime)
	if !now.Before(expires) {
		return Ticket{}, fmt.Errorf("mint ticket: %w", ErrExpired)
	}
	raw, err := k.sign(claims{
		Workspace: c.Workspace.String(),
		Session:   c.Session.String(),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   c.User.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expires),
		},
	})
	if err != nil {
		return Ticket{}, fmt.Errorf("mint ticket: %w", err)
	}
	return Ticket{secret.New[Ticket](raw)}, nil
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (k Key) sign(cl claims) (string, error) {
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, cl).SignedString(k.secret)
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}
	return raw, nil
}

// Verify checks a ticket's signature, algorithm and times, and returns the
// caller it names, with its session and when it expires. A ticket past its
// lifetime is ErrExpired.
func (k Key) Verify(raw string, now time.Time) (access.Caller, error) {
	c, _, err := k.verify(raw, now)
	return c, err
}

// VerifyToEnd is Verify for a request that ends a ticket's session: a ticket
// voice-guide signed is taken up to EndGrace past its lifetime too, so the
// session ends at once and its summary runs, rather than when the idle sweep
// finds it. Every other check still holds, as of the ticket's last moment.
func (k Key) VerifyToEnd(raw string, now time.Time) (access.Caller, error) {
	c, expires, err := k.verify(raw, now)
	if errors.Is(err, ErrExpired) && now.Sub(expires) <= EndGrace {
		c, _, err = k.verify(raw, expires)
	}
	return c, err
}

// verify is Verify, also returning, for a ticket past its lifetime, when it
// expired.
func (k Key) verify(raw string, now time.Time) (access.Caller, time.Time, error) {
	var cl claims
	parser := jwt.NewParser(
		// Only the algorithm voice-guide signs with: accepting what the ticket
		// claims would let an unsigned or differently signed ticket through.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		// Refusing an issue time in the future is what makes the lifetime cap
		// below a cap on how long a ticket really works.
		jwt.WithIssuedAt(),
		jwt.WithLeeway(leeway),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	if _, err := parser.ParseWithClaims(raw, &cl, func(*jwt.Token) (any, error) { return k.secret, nil }); err != nil {
		// The parser checks the signature before the times, so only a ticket
		// voice-guide signed is reported expired.
		if errors.Is(err, jwt.ErrTokenExpired) {
			return access.Caller{}, cl.ExpiresAt.Time, fmt.Errorf("verify ticket: %w", ErrExpired)
		}
		return access.Caller{}, time.Time{}, fmt.Errorf("verify ticket: %w", err)
	}
	c, err := callerOf(cl)
	if err != nil {
		return access.Caller{}, time.Time{}, fmt.Errorf("verify ticket: %w", err)
	}
	return c, time.Time{}, nil
}

// callerOf returns the caller named by the claims of a ticket whose signature
// and times were checked, refusing what voice-guide never mints.
func callerOf(cl claims) (access.Caller, error) {
	if cl.IssuedAt == nil {
		return access.Caller{}, errors.New("no issued-at time")
	}
	if cl.ExpiresAt.Sub(cl.IssuedAt.Time) > Lifetime {
		return access.Caller{}, fmt.Errorf("lifetime over %s", Lifetime)
	}
	var c access.Caller
	var err error
	if c.Workspace, err = uuid.Parse(cl.Workspace); err != nil {
		return access.Caller{}, fmt.Errorf("workspace: %w", err)
	}
	if c.User, err = uuid.Parse(cl.Subject); err != nil {
		return access.Caller{}, fmt.Errorf("user: %w", err)
	}
	// The nil UUID names no one; a ticket for it is a minting mistake.
	if c.Workspace == uuid.Nil() || c.User == uuid.Nil() {
		return access.Caller{}, errors.New("the nil UUID as workspace or user")
	}
	if c.Session, err = uuid.Parse(cl.Session); err != nil || c.Session == uuid.Nil() {
		return access.Caller{}, errors.New("a ticket names a session")
	}
	c.Expires = cl.ExpiresAt.UTC()
	return c, nil
}
