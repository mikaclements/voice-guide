package ticket_test

import (
	"encoding/base64"
	"encoding/json"
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

var (
	secret = []byte("0123456789abcdef0123456789abcdef")
	now    = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	caller = access.Caller{
		Workspace: uuid.MustParse("01a1de8a-0000-7000-8000-000000000001"),
		User:      uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a1"),
	}
)

func newKey(t *testing.T, s []byte) ticket.Key {
	t.Helper()
	k, err := ticket.NewKey(s)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return k
}

// ticketClaims are the claims of a good ticket for ticketCaller issued at iat, as a map a test
// can break one claim of.
func ticketClaims(iat, exp time.Time) jwt.MapClaims {
	return jwt.MapClaims{
		"ws": ticketCaller.Workspace.String(), "sub": ticketCaller.User.String(), "sid": ticketCaller.Session.String(),
		"iat": iat.Unix(), "exp": exp.Unix(),
	}
}

func TestVerifyRefusesForgedAndMalformedTickets(t *testing.T) {
	k := newKey(t, secret)
	other := newKey(t, []byte("fedcba9876543210fedcba9876543210"))
	valid := mintTicket(t, k, ticketCaller, now)
	fromOtherKey := mintTicket(t, other, ticketCaller, now)
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, ticketClaims(now, now.Add(time.Minute))).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none: %v", err)
	}
	signed := func(claims jwt.MapClaims) string {
		s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return s
	}
	without := func(claim string) string {
		c := ticketClaims(now, now.Add(time.Minute))
		delete(c, claim)
		return signed(c)
	}
	// Another user's ID in the payload, with the original header and signature.
	parts := strings.Split(valid, ".")
	forged := ticketClaims(now, now.Add(time.Hour))
	forged["sub"] = "01a1de8a-0000-7000-8000-0000000000a2"
	payload, err := json.Marshal(forged)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	editedClaims := parts[0] + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + parts[2]
	hs512, err := jwt.NewWithClaims(jwt.SigningMethodHS512, ticketClaims(now, now.Add(time.Minute))).SignedString(secret)
	if err != nil {
		t.Fatalf("sign HS512: %v", err)
	}

	tests := []struct {
		name string
		raw  string
		at   time.Time
	}{
		{"signed with another key", fromOtherKey, now},
		{"signature edited", valid[:len(valid)-2] + "xx", now},
		{"another user named after signing", editedClaims, now},
		{"unsigned", unsigned, now},
		{"another algorithm", hs512, now},
		{"issued in the future", signed(ticketClaims(now.Add(time.Hour), now.Add(2*time.Hour))), now},
		{"missing workspace", without("ws"), now},
		{"missing user", without("sub"), now},
		{"missing session", without("sid"), now},
		{"the nil UUID as workspace", signed(func() jwt.MapClaims {
			c := ticketClaims(now, now.Add(time.Minute))
			c["ws"] = uuid.Nil().String()
			return c
		}()), now},
		{"the nil UUID as user", signed(func() jwt.MapClaims {
			c := ticketClaims(now, now.Add(time.Minute))
			c["sub"] = uuid.Nil().String()
			return c
		}()), now},
		{"missing expiry", without("exp"), now},
		{"missing issued-at", without("iat"), now},
		{"not a ticket", "hello", now},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := k.Verify(tt.raw, tt.at); err == nil {
				t.Error("Verify accepted a ticket it should refuse")
			}
		})
	}
}

func TestVerifyToleratesAVerifierClockSlightlyBehind(t *testing.T) {
	k := newKey(t, secret)
	raw := mintTicket(t, k, ticketCaller, now)
	if _, err := k.Verify(raw, now.Add(-2*time.Second)); err != nil {
		t.Errorf("Verify 2s behind the minter: %v", err)
	}
}

func TestAKeyNeverPrintsItsSecret(t *testing.T) {
	k := newKey(t, secret)
	var logged strings.Builder
	slog.New(slog.NewTextHandler(&logged, nil)).Info("config", "key", k)
	for _, out := range []string{fmt.Sprint(k), fmt.Sprintf("%+v", k), logged.String()} {
		if strings.Contains(out, string(secret)) {
			t.Errorf("secret printed: %s", out)
		}
	}
}

func TestNewKeyRefusesShortSecrets(t *testing.T) {
	if _, err := ticket.NewKey([]byte(strings.Repeat("k", 31))); err == nil {
		t.Error("NewKey accepted a 31-byte secret")
	}
}
