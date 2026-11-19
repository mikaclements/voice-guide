// Package http posts deliveries to products' servers: conversation's webhook
// adapter. It sends each body as JSON with the headers the core signed, and
// follows no redirect.
package http

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/mikaclements/voice-guide/internal/conversation"
)

// Deadline bounds one attempt, from connecting to the answer's status: a
// product's server should answer at once and do its work afterwards, so 15 s
// leaves room for a slow start while a run, bounded at 30 s, still reads the
// session and saves how the attempt ended around it. A round guess.
const Deadline = 15 * time.Second

// connectTimeout bounds connecting, within Deadline: a server that takes
// longer to accept a connection is down or unreachable, and giving up after 5 s
// frees the worker for the next delivery 10 s sooner. A round guess, well past
// a connection's round trip across the world.
const connectTimeout = 5 * time.Second

// drainBytes bounds how much of an answer's body is read, only so its
// connection can be used again; the body itself is never looked at.
const drainBytes = 64 << 10

// The errors name no address: a URL can hold a secret of the product's, and
// errors reach logs and the job's record.
var (
	errNoAnswer    = errors.New("the product's server answered nothing within the deadline")
	errUnreachable = errors.New("the product's server couldn't be reached")
)

// Poster posts deliveries over HTTP.
type Poster struct {
	client   *http.Client
	deadline time.Duration
}

// dialer opens a connection to a product's server.
type dialer func(ctx context.Context, network, address string) (net.Conn, error)

// New returns a poster whose every attempt is bounded by deadline, and its
// connecting by connectTimeout within it.
func New(deadline time.Duration) *Poster {
	return newPoster(deadline, connectTimeout, (&net.Dialer{}).DialContext)
}

func newPoster(deadline, connect time.Duration, dial dialer) *Poster {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, connect)
		defer cancel()
		return dial(ctx, network, address)
	}
	return &Poster{deadline: deadline, client: &http.Client{
		Transport: transport,
		// A redirect is answered as it is: following it would post the session, and its
		// signature, wherever the product's server pointed, and its URL should be the final one.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Post sends d's body as JSON with its headers, and returns the status the
// product's server answered.
func (p *Poster) Post(ctx context.Context, d conversation.Delivery) (int, error) {
	attempt, cancel := context.WithTimeout(ctx, p.deadline)
	defer cancel()
	req, err := http.NewRequestWithContext(attempt, http.MethodPost, d.URL, bytes.NewReader(d.Body))
	if err != nil {
		return 0, errUnreachable
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range d.Headers {
		req.Header.Set(name, value)
	}
	resp, err := p.client.Do(req)
	switch {
	case err != nil && ctx.Err() != nil:
		return 0, ctx.Err() // stopped from outside, not the product's server's doing
	case err != nil && attempt.Err() != nil:
		return 0, errNoAnswer
	case err != nil:
		return 0, errUnreachable
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, drainBytes))
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
