// Package access says who a request comes from and which session their
// credential lets them use. It imports nothing of voice-guide's, so the
// API's router, the ticket check, conversation and speech name a caller
// without the router or the tickets importing a module.
package access

import (
	"time"
	"uuid"
)

// Caller is who a request comes from: a user in a workspace, limited to one
// session by that session's ticket, or a product's server,
// proven by its product key, acting for its workspace. Every request is
// checked against it, so adding logins changes how a caller is proven, not
// what is checked.
type Caller struct {
	Workspace uuid.UUID
	// User is the user a ticket names; a product's server names none, and
	// names the user in what it asks for instead.
	User uuid.UUID
	// Session is the one session a ticket works in, or the nil UUID for a
	// caller that holds no ticket.
	Session uuid.UUID
	// Expires is when a ticket stops working, so an audio connection it
	// opened ends then too; zero for a caller that holds no ticket.
	Expires time.Time
	// Product says the caller is a product's server, proven by its key.
	Product bool
}

// ExpiredAt reports whether the caller's ticket has expired by now. It is for
// a route that takes only tickets: a caller that holds none has expired.
func (c Caller) ExpiredAt(now time.Time) bool {
	return !now.Before(c.Expires)
}
