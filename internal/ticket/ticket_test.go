package ticket_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/golang-jwt/jwt/v5"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/ticket"
)

var ticketCaller = access.Caller{
	Workspace: caller.Workspace,
	User:      caller.User,
	Session:   uuid.MustParse("01a1de8a-0000-7000-8000-0000000000e1"),
}

func mintTicket(t *testing.T, k ticket.Key, c access.Caller, at time.Time) string {
	t.Helper()
	ticket, err := k.MintTicket(c, at, at)
	if err != nil {
		t.Fatalf("MintTicket: %v", err)
	}
	return ticket.Reveal()
}

func TestATicketVerifiesToItsSessionsCallerAndWhenItExpires(t *testing.T) {
	k := newKey(t, secret)
	got, err := k.Verify(mintTicket(t, k, ticketCaller, now), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	want := ticketCaller
	want.Expires = now.Add(4 * time.Hour)
	if got != want {
		t.Errorf("Verify = %+v, want %+v", got, want)
	}
}

// A claim this version doesn't read, written by one that did, is ignored, so
// a ticket minted before a deploy that stops reading it works until it expires.
func TestATicketWithAClaimNoLongerReadStillVerifies(t *testing.T) {
	k := newKey(t, secret)
	claims := ticketClaims(now, now.Add(4*time.Hour))
	claims["agent"] = "01a1de8a-0000-7000-8000-0000000000d1"
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	want := ticketCaller
	want.Expires = now.Add(4 * time.Hour)
	if got, err := k.Verify(raw, now.Add(time.Hour)); err != nil || got != want {
		t.Errorf("Verify = %+v, %v; want %+v", got, err, want)
	}
}

// A ticket minted later in its session, as by a repeat of the start, expires
// when the session's first did: four hours after the session started.
func TestATicketExpiresFourHoursAfterItsSessionStartedWhenEverMinted(t *testing.T) {
	k := newKey(t, secret)
	minted, err := k.MintTicket(ticketCaller, now, now.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("MintTicket: %v", err)
	}
	if got, err := k.Verify(minted.Reveal(), now.Add(3*time.Hour)); err != nil || !got.Expires.Equal(now.Add(4*time.Hour)) {
		t.Errorf("Verify = %+v, %v; want it to expire four hours after the session started", got, err)
	}
	if _, err := k.Verify(minted.Reveal(), now.Add(4*time.Hour+6*time.Second)); !errors.Is(err, ticket.ErrExpired) {
		t.Errorf("Verify past the session's four hours = %v, want ErrExpired", err)
	}
	// A database clock a second ahead of voice's records a start after now:
	// the ticket still lives four hours, not a second over the cap Verify holds.
	ahead, err := k.MintTicket(ticketCaller, now.Add(time.Second), now)
	if err != nil {
		t.Fatalf("MintTicket: %v", err)
	}
	if got, err := k.Verify(ahead.Reveal(), now); err != nil || !got.Expires.Equal(now.Add(4*time.Hour)) {
		t.Errorf("Verify with the start a second ahead = %+v, %v; want four hours from now", got, err)
	}
	// Once the session's four hours have passed, no ticket is minted for it,
	// rather than one already expired when issued.
	if _, err := k.MintTicket(ticketCaller, now, now.Add(4*time.Hour)); !errors.Is(err, ticket.ErrExpired) {
		t.Errorf("MintTicket at the session's four hours = %v, want ErrExpired", err)
	}
}

// Ending is the one thing a ticket still does past its lifetime, and only for
// 15 minutes, written out rather than read from EndGrace, so a user who stayed
// on can end what they started; every other check still holds.
func TestVerifyToEndTakesAGenuineTicketUpTo15MinutesPastItsLifetime(t *testing.T) {
	k := newKey(t, secret)
	raw := mintTicket(t, k, ticketCaller, now)
	late := now.Add(4*time.Hour + 15*time.Minute)
	got, err := k.VerifyToEnd(raw, late)
	if want := ticketCaller; err != nil || got.Session != want.Session || got.User != want.User || !got.Expires.Equal(now.Add(4*time.Hour)) {
		t.Errorf("VerifyToEnd 15 minutes past the lifetime = %+v, %v; want the ticket's caller", got, err)
	}
	if _, err := k.VerifyToEnd(raw, late.Add(time.Second)); !errors.Is(err, ticket.ErrExpired) {
		t.Errorf("VerifyToEnd a second later = %v, want ErrExpired", err)
	}
	signed := func(claims jwt.MapClaims) string {
		s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return s
	}
	for name, raw := range map[string]string{
		"signed with a key since rotated": mintTicket(t, newKey(t, []byte("fedcba9876543210fedcba9876543210")), ticketCaller, now),
		"meant to live over four hours": signed(jwt.MapClaims{
			"ws": ticketCaller.Workspace.String(), "sub": ticketCaller.User.String(), "sid": ticketCaller.Session.String(),
			"iat": now.Unix(), "exp": now.Add(4*time.Hour + time.Minute).Unix(),
		}),
	} {
		if _, err := k.VerifyToEnd(raw, late); err == nil {
			t.Errorf("%s: VerifyToEnd accepted it", name)
		}
	}
}

// The cap is four hours, written out rather than read from Lifetime, so
// raising the constant fails this test instead of moving it along. A clock up
// to the leeway behind or ahead of the minter's still takes it.
func TestATicketWorksForFourHoursFromIssueThenHasExpired(t *testing.T) {
	k := newKey(t, secret)
	raw := mintTicket(t, k, ticketCaller, now)
	for _, at := range []time.Time{now.Add(-2 * time.Second), now.Add(4 * time.Hour), now.Add(4*time.Hour + 4*time.Second)} {
		if _, err := k.Verify(raw, at); err != nil {
			t.Errorf("Verify %s after issue: %v", at.Sub(now), err)
		}
	}
	if _, err := k.Verify(raw, now.Add(4*time.Hour+6*time.Second)); !errors.Is(err, ticket.ErrExpired) {
		t.Errorf("Verify past the cap and the leeway = %v, want ErrExpired", err)
	}
}

func TestVerifyRefusesTicketsItShouldNotTrust(t *testing.T) {
	k := newKey(t, secret)
	claims := func(edit func(jwt.MapClaims)) string {
		c := jwt.MapClaims{
			"ws": ticketCaller.Workspace.String(), "sub": ticketCaller.User.String(), "sid": ticketCaller.Session.String(),
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		}
		edit(c)
		s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(secret)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return s
	}
	tests := map[string]string{
		"signed with a key since rotated": mintTicket(t, newKey(t, []byte("fedcba9876543210fedcba9876543210")), ticketCaller, now),
		"meant to live over four hours":   claims(func(c jwt.MapClaims) { c["exp"] = now.Add(4*time.Hour + time.Second).Unix() }),
		"the nil UUID as session":         claims(func(c jwt.MapClaims) { c["sid"] = uuid.Nil().String() }),
		"a session that isn't a UUID":     claims(func(c jwt.MapClaims) { c["sid"] = "lamp" }),
		"no user":                         claims(func(c jwt.MapClaims) { delete(c, "sub") }),
		"the nil UUID as workspace":       claims(func(c jwt.MapClaims) { c["ws"] = uuid.Nil().String() }),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := k.Verify(raw, now)
			if err == nil {
				t.Fatal("Verify accepted a ticket it should refuse")
			}
			if errors.Is(err, ticket.ErrExpired) {
				t.Errorf("Verify = %v, want a refusal other than expiry", err)
			}
		})
	}
}

// Only a ticket voice-guide signed is reported expired: one signed with another
// key, past its time too, is refused as any other forgery, so the page never
// tells the user to start again over a ticket that was never good.
func TestAnExpiredTicketFromAnotherKeyIsNotReportedExpired(t *testing.T) {
	raw := mintTicket(t, newKey(t, []byte("fedcba9876543210fedcba9876543210")), ticketCaller, now)
	if _, err := newKey(t, secret).Verify(raw, now.Add(5*time.Hour)); err == nil || errors.Is(err, ticket.ErrExpired) {
		t.Errorf("Verify = %v, want a refusal other than expiry", err)
	}
}

func TestMintTicketRefusesACallerWithoutItsSessionWorkspaceOrUser(t *testing.T) {
	k := newKey(t, secret)
	for name, edit := range map[string]func(*access.Caller){
		"no session":   func(c *access.Caller) { c.Session = uuid.Nil() },
		"no user":      func(c *access.Caller) { c.User = uuid.Nil() },
		"no workspace": func(c *access.Caller) { c.Workspace = uuid.Nil() },
		"a product":    func(c *access.Caller) { c.Product = true },
	} {
		c := ticketCaller
		edit(&c)
		if _, err := k.MintTicket(c, now, now); err == nil {
			t.Errorf("%s: MintTicket made a ticket", name)
		}
	}
}

func TestATicketNeverPrintsItself(t *testing.T) {
	k := newKey(t, secret)
	minted, err := k.MintTicket(ticketCaller, now, now)
	if err != nil {
		t.Fatalf("MintTicket: %v", err)
	}
	held := struct{ T ticket.Ticket }{minted}
	var logged strings.Builder
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("start", "ticket", minted, "held", held)
	encoded, err := json.Marshal(held)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, out := range []string{
		fmt.Sprint(minted), fmt.Sprintf("%v %+v %#v %s %q %x", minted, minted, minted, minted, minted, minted),
		fmt.Sprintf("%v %+v %#v", held, held, held), logged.String(), string(encoded),
	} {
		if strings.Contains(out, minted.Reveal()) {
			t.Errorf("ticket printed: %s", out)
		}
	}
}
