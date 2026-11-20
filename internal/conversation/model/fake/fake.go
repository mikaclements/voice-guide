// Package fake answers every turn with set text at a set pace: conversation's fake
// model, for the core's tests and for load tests.
package fake

import (
	"context"
	"sync"
	"time"

	"github.com/mikaclements/voice-guide/internal/conversation"
)

// Model sends the same pieces of text in reply to every request, one every
// pace, then done, or an error of kind Fail if one is set before the replies.
// It records each request and each reply its caller stopped.
type Model struct {
	Pace   time.Duration
	Pieces []string
	// Answers, when set, are sent one whole per request, in order, in place of
	// Pieces, until they run out.
	Answers []string
	Fail    conversation.ModelErrorKind
	// Report goes with done; unset, it names the fake as the provider and the
	// model asked for.
	Report *conversation.ModelReport

	mu        sync.Mutex
	requests  []conversation.ModelRequest
	cancelled int
}

// Reply sends on an unbuffered channel, so a caller that isn't reading holds
// the reply back.
func (m *Model) Reply(ctx context.Context, r conversation.ModelRequest) <-chan conversation.ModelEvent {
	m.mu.Lock()
	m.requests = append(m.requests, r)
	pieces := m.Pieces
	if len(m.Answers) > 0 {
		pieces, m.Answers = m.Answers[:1], m.Answers[1:]
	}
	m.mu.Unlock()
	events := make(chan conversation.ModelEvent)
	go func() {
		defer close(events)
		for _, piece := range pieces {
			if !m.send(ctx, events, conversation.ModelEvent{Text: piece}) {
				return
			}
		}
		report := conversation.ModelReport{Provider: "fake", Model: r.Model}
		if m.Report != nil {
			report = *m.Report
		}
		m.send(ctx, events, conversation.ModelEvent{Done: m.Fail == "", Report: &report, Error: m.Fail})
	}()
	return events
}

func (m *Model) send(ctx context.Context, events chan<- conversation.ModelEvent, e conversation.ModelEvent) bool {
	select {
	case <-time.After(m.Pace):
	case <-ctx.Done():
	}
	// Checked before sending too: a caller draining the channel after it
	// stopped the reply must not receive the rest as if it hadn't.
	if ctx.Err() == nil {
		select {
		case events <- e:
			return true
		case <-ctx.Done():
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelled++
	return false
}

// Requests returns the requests made so far, in order, and how many replies
// their caller stopped before the end.
func (m *Model) Requests() ([]conversation.ModelRequest, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]conversation.ModelRequest(nil), m.requests...), m.cancelled
}
