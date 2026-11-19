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
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/secret"
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

// ErrUnknown reports a key that is malformed, never made or revoked. All look
// alike, so a caller learns nothing about which keys exist.
var ErrUnknown = errors.New("unknown product key")

// Key is a product key itself, as the product's server sends it. It formats,
// logs and encodes redacted, so a key passed around never reaches a log or an
// error; Reveal gives the key, to show it once or to send it. The key sits
// behind a pointer, so even printing a struct that holds a Key where its
// methods can't be called shows an address, not the key.
type Key struct {
	heldKey
}

// heldKey is the key a Key holds, embedded under a name no other package can
// reach, so Key's methods are its only way out.
type heldKey = secret.Value[Key]

// NewKey holds raw, as sent, as a product key.
func NewKey(raw string) Key { return Key{secret.New[Key](raw)} }

// Record is what is kept of a product key.
type Record struct {
	ID        uuid.UUID
	Workspace uuid.UUID
	// Hash is the key's SHA-256.
	Hash []byte
	// Display is the key's first characters.
	Display   string
	CreatedAt time.Time
	// RevokedAt is when the key stopped working: zero while it works.
	RevokedAt time.Time
}

// Store keeps what is kept of product keys.
type Store interface {
	// Add saves r.
	Add(ctx context.Context, r Record) error
	// Workspace returns the workspace of the working key with the hash. It
	// looks across workspaces: the key is what names the workspace.
	Workspace(ctx context.Context, hash []byte) (uuid.UUID, bool, error)
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

// Create makes a key for the workspace and returns it, the only time the key
// itself is seen, with what is kept of it.
func (k *Keys) Create(ctx context.Context, workspace uuid.UUID) (Key, Record, error) {
	random := make([]byte, randomBytes)
	_, _ = rand.Read(random) // never fails: it crashes the program instead
	raw := prefix + encoding.EncodeToString(random)
	r := Record{ID: uuid.NewV7(), Workspace: workspace, Hash: hash(raw), Display: raw[:displayLen]}
	if err := k.store.Add(ctx, r); err != nil {
		return Key{}, Record{}, fmt.Errorf("save the product key: %w", err)
	}
	return NewKey(raw), r, nil
}

// Workspace returns the workspace the key acts for: ErrUnknown for a key that
// doesn't work, and any other error when the check couldn't be made, so a
// product isn't told its key is wrong while the store is down.
func (k *Keys) Workspace(ctx context.Context, key Key) (uuid.UUID, error) {
	raw := key.Reveal()
	if !wellFormed(raw) {
		return uuid.Nil(), ErrUnknown
	}
	ws, found, err := k.store.Workspace(ctx, hash(raw))
	switch {
	case err != nil:
		return uuid.Nil(), fmt.Errorf("check the product key: %w", err)
	case !found:
		return uuid.Nil(), ErrUnknown
	}
	return ws, nil
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
