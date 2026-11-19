// Package fake stands in for products' servers: conversation's webhook fake,
// for the core's tests and for load tests. It keeps what was posted and
// answers as it was told.
package fake

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"sync"

	"github.com/mikaclements/voice-guide/internal/conversation"
)

// Answer is how the product's server answers one post: a status, or an
// error when it answered nothing.
type Answer struct {
	Status int
	Err    error
}

// Poster keeps each delivery posted and gives the answers it was given, in
// order, then 200 OK. It is safe for concurrent use.
type Poster struct {
	mu      sync.Mutex
	answers []Answer
	posted  []conversation.Delivery
}

// New returns a poster that gives answers, in order, then 200 OK.
func New(answers ...Answer) *Poster { return &Poster{answers: answers} }

// Post keeps d and gives the next answer.
func (p *Poster) Post(ctx context.Context, d conversation.Delivery) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	d.Body, d.Headers = slices.Clone(d.Body), maps.Clone(d.Headers)
	p.posted = append(p.posted, d)
	if len(p.answers) == 0 {
		return http.StatusOK, nil
	}
	a := p.answers[0]
	p.answers = p.answers[1:]
	return a.Status, a.Err
}

// Posted returns every delivery posted, in order.
func (p *Poster) Posted() []conversation.Delivery {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.posted)
}
