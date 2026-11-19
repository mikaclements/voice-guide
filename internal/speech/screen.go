package speech

import (
	"slices"
	"sync"

	"github.com/mikaclements/voice-guide/internal/conversation"
)

// screenWaiting holds what the page's screen shows, sent on its own on one connection's data
// channel, until a turn taken on that connection is saved with it: the newest block of each name,
// and the number of the last screen message whose blocks it holds. The data channel's reader adds
// to it while a turn's goroutine reads it; a connection's turns run one at a time, so a block a
// turn is carrying still counts against the limit, and a turn not saved leaves it for the next.
type screenWaiting struct {
	mu     sync.Mutex
	blocks []conversation.ScreenInputBlock
	upTo   int
}

// add keeps the blocks of screen message n, each in place of the one of its name waiting, once
// check passes them alone and with the blocks still waiting, as a turn's screen input would be; a
// message refused changes nothing.
func (w *screenWaiting) add(blocks []conversation.ScreenInputBlock, n int, check func([]conversation.ScreenInputBlock) error) error {
	if err := check(blocks); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	kept := withNewer(w.blocks, blocks)
	if err := check(kept); err != nil {
		return err
	}
	w.blocks, w.upTo = kept, max(w.upTo, n)
	return nil
}

// take returns a copy of the blocks waiting, with the number of the last screen message they came
// in, or nothing; they stay until saved lets them go.
func (w *screenWaiting) take() ([]conversation.ScreenInputBlock, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.blocks) == 0 {
		return nil, 0
	}
	return slices.Clone(w.blocks), w.upTo
}

// saved lets go of the blocks a turn was saved with, each unless a newer one of its name has come
// since.
func (w *screenWaiting) saved(blocks []conversation.ScreenInputBlock) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.blocks = slices.DeleteFunc(w.blocks, func(b conversation.ScreenInputBlock) bool { return slices.Contains(blocks, b) })
}

// withNewer returns the blocks of older whose name newer doesn't have, then newer's as they are,
// so a name newer sends twice is still refused as sent twice.
func withNewer(older, newer []conversation.ScreenInputBlock) []conversation.ScreenInputBlock {
	kept := make([]conversation.ScreenInputBlock, 0, len(older)+len(newer))
	for _, b := range older {
		if !slices.ContainsFunc(newer, func(n conversation.ScreenInputBlock) bool { return n.Name == b.Name }) {
			kept = append(kept, b)
		}
	}
	return append(kept, newer...)
}

// screenChanged keeps the blocks of a screen message for the next turn taken on the connection,
// or refuses them, so the page can tell the product; nothing of a message refused waits.
func (r *replies) screenChanged(m turnIn) {
	blocks := make([]conversation.ScreenInputBlock, 0, len(m.ScreenInput))
	for _, b := range m.ScreenInput {
		blocks = append(blocks, conversation.ScreenInputBlock(b))
	}
	if err := r.screen.add(blocks, m.N, r.s.conversation.CheckScreenInput); err != nil {
		r.s.tell(r.conn, out{Type: "screen_refused", N: m.N, Detail: err.Error()})
	}
}
