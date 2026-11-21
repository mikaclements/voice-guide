// Package productkey makes and checks product keys: what a product's server
// sends to prove which workspace it acts for. Only each key's SHA-256 is kept,
// so a copy of the database can't be used to act as a product. It sits outside
// the modules, as the ticket check does, so the API's router checks keys
// without importing a module.
package productkey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
)

// prefix marks a product key, so the router tells it from a ticket at a glance
// and a key pasted where it shouldn't be is easy to spot.
const prefix = "vgk_"

// randomBytes is a key's randomness: 256 bits can't be guessed, so a fast hash
// keeps it as safe as a slow one, which only protects secrets people choose.
const randomBytes = 32

var encoding = base64.RawURLEncoding

// keyLen is a key's length: the prefix, then its randomness in base64url.
var keyLen = len(prefix) + encoding.EncodedLen(randomBytes)

// displayLen is how many of a key's first characters are kept to tell keys
// apart: the prefix and 8 characters, 48 of the 256 bits, too few to help
// guess the rest.
const displayLen = len(prefix) + 8

// redacted is how a key shows itself everywhere but Reveal.
const redacted = "productkey.Key(redacted)"

// ErrUnknown reports a key that is malformed, never made or revoked. All look
// alike, so a caller learns nothing about which keys exist.
var ErrUnknown = errors.New("unknown product key")

// Key is a product key itself, as the product's server sends it. It formats,
// logs and encodes redacted, so a key passed around never reaches a log or an
// error; Reveal gives the key, to show it once or to send it. The key sits
// behind a pointer, so even printing a struct that holds a Key where its
// methods can't be called shows an address, not the key.
type Key struct {
	raw *string
}

// NewKey holds raw, as sent, as a product key.
func NewKey(raw string) Key { return Key{raw: &raw} }

// Reveal returns the key itself.
func (k Key) Reveal() string {
	if k.raw == nil {
		return ""
	}
	return *k.raw
}

// String keeps the key out of formatted output.
func (Key) String() string { return redacted }

// GoString keeps the key out of %#v.
func (Key) GoString() string { return redacted }

// Format keeps the key out of every formatting verb.
func (Key) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, redacted) }

// LogValue keeps the key out of logs.
func (Key) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON keeps the key out of anything encoded as JSON.
func (Key) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// Record is what is kept of a product key.
type Record struct {
	ID        uuid.UUID
	Workspace uuid.UUID
	// Hash is the key's SHA-256.
	Hash []byte
	// Display is the key's first characters.
	Display string
	// Permissions is what the key lets its holder do, fixed when it is made:
	// to change them, a new key is made and the old one revoked.
	Permissions access.Permissions
	CreatedAt   time.Time
	// RevokedAt is when the key stopped working: zero while it works.
	RevokedAt time.Time
}

// Store keeps what is kept of product keys.
type Store interface {
	// Add saves r.
	Add(ctx context.Context, r Record) error
	// Find returns the working key with the hash, with at least its
	// workspace and permissions. It looks across workspaces: the key is what
	// names the workspace.
	Find(ctx context.Context, hash []byte) (Record, bool, error)
	// List returns the workspace's keys, newest first.
	List(ctx context.Context, workspace uuid.UUID) ([]Record, error)
	// Revoke stops the workspace's key with id working, if it hasn't already,
	// and says whether the workspace has such a key.
	Revoke(ctx context.Context, workspace, id uuid.UUID) (bool, error)
	// Working counts the workspace's keys that work.
	Working(ctx context.Context, workspace uuid.UUID) (int, error)
}

// Keys makes and checks product keys.
type Keys struct {
	store Store
}

// NewKeys returns the keys kept in store.
func NewKeys(store Store) *Keys { return &Keys{store: store} }

// Looks says whether raw has a product key's form rather than a ticket's, so
// the router knows which check it needs.
func Looks(raw string) bool { return strings.HasPrefix(raw, prefix) }

// Create makes a key for the workspace holding the permissions and returns
// it, the only time the key itself is seen, with what is kept of it.
func (k *Keys) Create(ctx context.Context, workspace uuid.UUID, permissions access.Permissions) (Key, Record, error) {
	if permissions == 0 {
		return Key{}, Record{}, errors.New("a product key needs at least one permission")
	}
	random := make([]byte, randomBytes)
	_, _ = rand.Read(random) // never fails: it crashes the program instead
	raw := prefix + encoding.EncodeToString(random)
	r := Record{ID: uuid.NewV7(), Workspace: workspace, Hash: hash(raw), Display: raw[:displayLen], Permissions: permissions}
	if err := k.store.Add(ctx, r); err != nil {
		return Key{}, Record{}, fmt.Errorf("save the product key: %w", err)
	}
	return NewKey(raw), r, nil
}

// Find returns what is kept of the key, naming the workspace it acts for and
// its permissions: ErrUnknown for a key that doesn't work, and any other error
// when the check couldn't be made, so a product isn't told its key is wrong
// while the store is down.
func (k *Keys) Find(ctx context.Context, key Key) (Record, error) {
	raw := key.Reveal()
	if !wellFormed(raw) {
		return Record{}, ErrUnknown
	}
	r, found, err := k.store.Find(ctx, hash(raw))
	switch {
	case err != nil:
		return Record{}, fmt.Errorf("check the product key: %w", err)
	case !found:
		return Record{}, ErrUnknown
	}
	return r, nil
}

// List returns what is kept of the workspace's keys, newest first.
func (k *Keys) List(ctx context.Context, workspace uuid.UUID) ([]Record, error) {
	return k.store.List(ctx, workspace)
}

// Revoke stops the workspace's key with id working for every check from now
// on, says whether the workspace has such a key, and counts its keys still
// working: with none left, the product's starts are refused. Revoking it
// again changes nothing. A start whose key was checked before still finishes,
// within its deadline. A key found and revoked is reported found even when
// counting fails.
func (k *Keys) Revoke(ctx context.Context, workspace, id uuid.UUID) (found bool, working int, err error) {
	if found, err = k.store.Revoke(ctx, workspace, id); err != nil || !found {
		return found, 0, err
	}
	working, err = k.store.Working(ctx, workspace)
	return true, working, err
}

func wellFormed(raw string) bool {
	if len(raw) != keyLen || !Looks(raw) {
		return false
	}
	_, err := encoding.DecodeString(raw[len(prefix):])
	return err == nil
}

func hash(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}
