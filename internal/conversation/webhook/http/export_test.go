package http

import (
	"context"
	"net"
	"time"
)

// NewDialing returns a poster that connects through dial, giving up on a
// connection after connect, and on an attempt after deadline.
func NewDialing(deadline, connect time.Duration, dial func(ctx context.Context, network, address string) (net.Conn, error)) *Poster {
	return newPoster(deadline, connect, dial)
}

// ConnectTimeout bounds connecting for the posters New returns.
const ConnectTimeout = connectTimeout
