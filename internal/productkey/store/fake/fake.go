// Package fake keeps product keys in memory: the product key store's fake for
// tests and load tests.
package fake

import (
	"bytes"
	"context"
	"slices"
	"sync"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/productkey"
)

// Keys keeps product keys in memory. It is safe for concurrent use.
type Keys struct {
	mu    sync.Mutex
	keys  []productkey.Record
	reads int
	err   error
}

// New returns an empty store.
func New() *Keys { return &Keys{} }

// Fail makes every call fail with err from now on, as a store that is down does.
func (f *Keys) Fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// Reads is how many key checks reached the store.
func (f *Keys) Reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

// Add saves r.
func (f *Keys) Add(_ context.Context, r productkey.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	r.CreatedAt = time.Now()
	f.keys = append(f.keys, r)
	return nil
}

// Find returns the working key with the hash.
func (f *Keys) Find(_ context.Context, hash []byte) (productkey.Record, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.err != nil {
		return productkey.Record{}, false, f.err
	}
	for _, k := range f.keys {
		if bytes.Equal(k.Hash, hash) && k.RevokedAt.IsZero() {
			return k, true, nil
		}
	}
	return productkey.Record{}, false, nil
}

// List returns the workspace's keys, newest first.
func (f *Keys) List(_ context.Context, workspace uuid.UUID) ([]productkey.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var out []productkey.Record
	for _, k := range slices.Backward(f.keys) {
		if k.Workspace == workspace {
			out = append(out, k)
		}
	}
	return out, nil
}

// Working counts the workspace's keys that work.
func (f *Keys) Working(_ context.Context, workspace uuid.UUID) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	n := 0
	for _, k := range f.keys {
		if k.Workspace == workspace && k.RevokedAt.IsZero() {
			n++
		}
	}
	return n, nil
}

// Revoke stops the workspace's key with id working, if it hasn't already.
func (f *Keys) Revoke(_ context.Context, workspace, id uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	for i, k := range f.keys {
		if k.Workspace == workspace && k.ID == id {
			if k.RevokedAt.IsZero() {
				f.keys[i].RevokedAt = time.Now()
			}
			return true, nil
		}
	}
	return false, nil
}
