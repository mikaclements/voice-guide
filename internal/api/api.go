// Package api is the public HTTP API's one router: the same API an outside
// product calls and voice-guide's own web page uses. Each module's HTTP
// adapter registers its routes here, and only through the Handle methods,
// which put every route behind the check of the caller's credential, a ticket
// or a product key, and a deadline of its own; every error is an RFC 9457
// problem details document. Without a credential the router answers only a
// browser's preflight for a session's route and the drop-in client's script.
// It also mints the ticket a start hands back.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/productkey"
	"github.com/mikaclements/voice-guide/internal/ticket"
)

// RequestTimeout bounds a request's work unless its route needs longer, so a
// stalled dependency can't hold a request open. A route that reads and writes
// the database a few times, or sets up an audio connection, takes
// milliseconds, so 10s is far beyond a healthy request: a round guess.
const RequestTimeout = 10 * time.Second

// lateWrite is how long past its deadline a route may still write, so a reply
// the deadline cut short can say so; the same margin a turn keeps for saving
// how it ended.
const lateWrite = 5 * time.Second

// ticketExpired is the problem type for a ticket past its lifetime, told
// apart from a ticket that is wrong: the page says the session has expired
// and to start a new one.
const ticketExpired = "/v1/problems/ticket-expired"

// sessionEnded is the problem type for a session that has ended, told apart from the 409 for
// another turn in flight.
const sessionEnded = "/v1/problems/session-ended"

// A session's routes answer a page on any origin, with no credentials mode:
// the ticket in the Authorization header is their only credential and no
// cookie is ever read, so a page on another site can do nothing with them that
// whoever holds the ticket couldn't do without a browser. The page sends these
// headers and may read these back: Location names an audio connection, which
// the page ends there, and Retry-After says when a full server takes another.
const (
	allowedHeaders = "Authorization, Content-Type"
	exposedHeaders = "Location, Retry-After"
)

// preflightMaxAge is how long, in seconds, a browser may reuse a preflight's
// answer for one address: 2 hours, a round figure past most sessions' length,
// so the typed turns after a session's first cost no preflight where the
// browser keeps an answer that long; one that keeps it less asks again later
// in the session. A change to what the routes take reaches browsers within it.
// Answers are kept per address, so each session's routes preflight once.
const preflightMaxAge = "7200"

// Handler handles a request from a caller whose credential was checked.
type Handler func(http.ResponseWriter, *http.Request, access.Caller)

// Router holds the API's routes.
type Router struct {
	mux    *http.ServeMux
	key    ticket.Key
	keys   *productkey.Keys
	now    func() time.Time
	log    *slog.Logger
	client *client  // the drop-in client, when served
	routes []string // each pattern registered, in order
}

// route is a registered route's handler, marked when a page on any site may
// call it, so a preflight is answered from the route alone.
type route struct {
	serve     http.HandlerFunc
	anyOrigin bool
}

func (h route) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.serve(w, r) }

// NewRouter returns a router with no routes. keys checks product keys; with
// none, every product key is refused. now is the clock tickets are checked
// against.
func NewRouter(key ticket.Key, keys *productkey.Keys, now func() time.Time, log *slog.Logger) *Router {
	return &Router{mux: http.NewServeMux(), key: key, keys: keys, now: now, log: log}
}

// Log is the logger the API's handlers share.
func (rt *Router) Log() *slog.Logger { return rt.log }

// Ticket returns a ticket for the caller's session, which started at
// started, issued now: a product's server hands it to the user's browser,
// which takes turns, opens the audio connection and ends the session with
// it, and with nothing else. ok is false once the session's tickets have
// expired: none is minted for it.
func (rt *Router) Ticket(c access.Caller, started time.Time) (t ticket.Ticket, ok bool, err error) {
	t, err = rt.key.MintTicket(c, started, rt.now())
	switch {
	case errors.Is(err, ticket.ErrExpired):
		return ticket.Ticket{}, false, nil
	case err != nil:
		return ticket.Ticket{}, false, err
	}
	return t, true, nil
}

// TicketExpired answers that the caller's ticket has passed its lifetime, as
// the credential check does, for a route whose own clock finds it so.
func (rt *Router) TicketExpired(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
	rt.TypedProblem(w, r, http.StatusUnauthorized, ticketExpired, "Ticket expired", "the session's ticket has expired; start a new session")
}

// SessionEnded answers that the session has ended, for a route whose session is over: sending
// it again never helps, which tells it from the 409 for another turn in flight.
func (rt *Router) SessionEnded(w http.ResponseWriter, r *http.Request) {
	rt.TypedProblem(w, r, http.StatusConflict, sessionEnded, "Session ended", "the session has ended; start a new one")
}

// takes is what credentials a route takes.
type takes struct {
	// keys: a product's key, naming its workspace and no user.
	keys bool
	// tickets: the ticket for the session the path names as {id}.
	tickets bool
	// pastLifetime: a ticket voice-guide signed shortly past its lifetime
	// too, on the session's end, so the session can still end at once.
	pastLifetime bool
	// need: the permissions a key must hold, when keys are taken.
	need access.Permissions
}

// HandleTicket registers h for the pattern behind the credential check and a
// deadline of its own, so no route can be added without them. It is for the
// routes in one session, which the user's browser calls: the pattern names
// the session as {id}, and a ticket works only on its own session's routes.
// They never take a product key.
func (rt *Router) HandleTicket(pattern string, timeout time.Duration, h Handler) {
	rt.handle(pattern, timeout, takes{tickets: true}, h)
}

// HandleSessionEnd registers h for the route that ends a session, as HandleTicket
// does, with two differences. The session's ticket works there for a while
// past its lifetime, so a page learning its ticket ran out, or a user who
// stayed on, ends the session at once and its summary runs, rather than when
// the idle sweep finds it. And the product's server may call it with a key
// holding need, to end any session of its workspace, as when a ticket was
// stolen.
func (rt *Router) HandleSessionEnd(pattern string, timeout time.Duration, need access.Permissions, h Handler) {
	rt.handle(pattern, timeout, takes{tickets: true, pastLifetime: true, keys: true, need: need}, h)
}

// HandleProduct registers h for a route a product's server calls: it takes
// the product's key, naming the workspace and no user, and never a ticket, and
// only a key holding need. It too has a deadline of its own.
func (rt *Router) HandleProduct(pattern string, timeout time.Duration, need access.Permissions, h Handler) {
	rt.handle(pattern, timeout, takes{keys: true, need: need}, h)
}

// Routes returns the method and path pattern of every route registered, such as
// "GET /v1/agents", so a test can hold the published description of the API
// to the routes the router really serves, without reading the code that
// registers them. The drop-in client's path is not among them: it is no route
// of the API.
func (rt *Router) Routes() []string { return slices.Clone(rt.routes) }

func (rt *Router) handle(pattern string, timeout time.Duration, what takes, h Handler) {
	if what.keys && what.need == 0 {
		// A route any key could call would let a key made only to publish run sessions, and the reverse.
		panic("api: " + pattern + " takes a product key but needs no permission")
	}
	rt.routes = append(rt.routes, pattern)
	// A route that takes a ticket is one a page calls, from whatever site embeds it.
	rt.mux.Handle(pattern, route{anyOrigin: what.tickets, serve: func(w http.ResponseWriter, r *http.Request) {
		if what.tickets {
			// On every answer, refusals included, so the page reads why it was refused.
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Expose-Headers", exposedHeaders)
		}
		// The deadline covers the key check too, which reads the database.
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		// The server's write timeout suits a short reply; it follows the route's
		// deadline instead, or a reply streaming for longer would be cut off
		// midway. A writer without a connection (a test's recorder) can't set it.
		deadline, _ := ctx.Deadline()
		_ = http.NewResponseController(w).SetWriteDeadline(deadline.Add(lateWrite))
		r = r.WithContext(ctx)
		if c, ok := rt.authenticate(w, r, what); ok && rt.inSession(w, r, c) {
			h(w, r, c)
		}
	}})
}

// inSession says whether the route is in the caller's session, when the
// caller holds a ticket, or answers the request and returns false. A ticket
// for another session is answered as a session that doesn't exist, so it
// learns nothing about other sessions, and a route whose path names no
// session refuses every ticket.
func (rt *Router) inSession(w http.ResponseWriter, r *http.Request, c access.Caller) bool {
	if c.Session == uuid.Nil() {
		return true
	}
	if id, err := uuid.Parse(r.PathValue("id")); err != nil || id != c.Session {
		rt.Problem(w, r, http.StatusNotFound, "no such session")
		return false
	}
	return true
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == ClientPath && rt.client != nil {
		rt.serveClient(w, r)
		return
	}
	if rt.preflight(w, r) {
		return
	}
	h, pattern := rt.mux.Handler(r)
	if pattern == "" {
		// No route matched. The mux would answer 404 or 405 in plain text; run its
		// handler only to learn which, and answer as a problem instead.
		c := &statusCapture{header: http.Header{}}
		h.ServeHTTP(c, r)
		if c.status == http.StatusMethodNotAllowed {
			w.Header().Set("Allow", c.header.Get("Allow"))
			rt.Problem(w, r, c.status, "")
			return
		}
		// Anything else, including the redirect the mux offers for an unclean
		// path that matches no route, is a path the API doesn't have.
		rt.Problem(w, r, http.StatusNotFound, "")
		return
	}
	rt.mux.ServeHTTP(w, r)
}

// preflight answers a browser asking, before a page on another site sends a
// request, whether it may, and says whether it did: yes for a route a page on
// any site may call. Any other OPTIONS request, a preflight for a product's
// route among them, is left to be refused as a method no route takes, so a
// browser never sends a product's route a request. The answer comes from the route's
// pattern alone: no handler runs and no credential is read, since a browser
// sends none with a preflight, and nothing the request asks for is echoed.
func (rt *Router) preflight(w http.ResponseWriter, r *http.Request) bool {
	method := r.Header.Get("Access-Control-Request-Method")
	if r.Method != http.MethodOptions || r.Header.Get("Origin") == "" || method == "" {
		return false
	}
	probe := r.WithContext(r.Context())
	probe.Method = method
	h, pattern := rt.mux.Handler(probe)
	if open, ok := h.(route); !ok || !open.anyOrigin {
		return false
	}
	routeMethod, _, _ := strings.Cut(pattern, " ")
	header := w.Header()
	header.Set("Access-Control-Allow-Origin", "*")
	header.Set("Access-Control-Allow-Methods", routeMethod)
	header.Set("Access-Control-Allow-Headers", allowedHeaders)
	header.Set("Access-Control-Max-Age", preflightMaxAge)
	w.WriteHeader(http.StatusNoContent)
	return true
}

// authenticate returns the caller the request's credential names, or answers
// the request and returns false. A product key is checked only on a route
// that takes one, so a key sent where a browser's ticket belongs costs no read.
func (rt *Router) authenticate(w http.ResponseWriter, r *http.Request, what takes) (access.Caller, bool) {
	scheme, raw, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	// The scheme is case-insensitive, so clients that send "bearer" work too.
	if !strings.EqualFold(scheme, "Bearer") || raw == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
		needs := "a ticket"
		switch {
		case what.keys && what.tickets:
			needs = "a ticket or a product key"
		case what.keys:
			needs = "a product key"
		}
		rt.Problem(w, r, http.StatusUnauthorized, "this route needs "+needs+" as a bearer credential")
		return access.Caller{}, false
	}
	refuse := func(reason, detail string) (access.Caller, bool) {
		// The reason goes to the log, not the reply; the credential itself goes nowhere.
		rt.log.InfoContext(r.Context(), "credential refused", "reason", reason)
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		rt.Problem(w, r, http.StatusUnauthorized, detail)
		return access.Caller{}, false
	}
	if !productkey.Looks(raw) {
		verify := rt.key.Verify
		if what.pastLifetime {
			verify = rt.key.VerifyToEnd
		}
		c, err := verify(raw, rt.now())
		switch {
		case errors.Is(err, ticket.ErrExpired):
			rt.log.InfoContext(r.Context(), "credential refused", "reason", err.Error())
			rt.TicketExpired(w, r)
			return access.Caller{}, false
		case err != nil && !what.tickets:
			return refuse(err.Error(), "this route takes a product key, and what was sent is not one")
		case err != nil:
			return refuse(err.Error(), "the ticket is invalid")
		case !what.tickets:
			return refuse("a ticket on a route that takes a product key", "this route takes a product key, not a ticket")
		}
		return c, true
	}
	if !what.keys || rt.keys == nil {
		return refuse("a product key on a route that takes a ticket", "this route takes a ticket, not a product key")
	}
	// A browser names the page's origin on every request a page on another site
	// makes, and a server's HTTP client names none. A key from a page has
	// reached every visitor, who could start sessions for any of the product's
	// users, and any page could have its visitors' browsers send guessed keys;
	// so a key sent with an origin is refused unread.
	if r.Header.Get("Origin") != "" {
		return refuse("a product key sent from a page", "a product key is for the product's server, never a page")
	}
	k, err := rt.keys.Find(r.Context(), productkey.NewKey(raw))
	switch {
	case errors.Is(err, productkey.ErrUnknown):
		return refuse(err.Error(), "the product key is invalid or revoked")
	case err != nil:
		// 503, not 401: the key may well work, so the product retries rather than replacing it.
		rt.log.ErrorContext(r.Context(), "check a product key", "err", err)
		rt.Problem(w, r, http.StatusServiceUnavailable, "the product key couldn't be checked; retry")
		return access.Caller{}, false
	case !k.Permissions.Has(what.need):
		// 403, not 401, as RFC 6750 asks: the key works, and sending it again never will here; the
		// product uses a key made with the permission named.
		rt.log.InfoContext(r.Context(), "credential refused", "reason", "a product key without the route's permission", "needs", what.need.String())
		// RFC 6750 lists the scope as space-separated names.
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="`+strings.Join(what.need.Names(), " ")+`"`)
		rt.Problem(w, r, http.StatusForbidden, "this route needs a product key with the "+what.need.String()+" permission")
		return access.Caller{}, false
	}
	return access.Caller{Workspace: k.Workspace, Product: true, Permissions: k.Permissions}, true
}

// PathID reads the ID in the request's path at name. One that isn't a UUID
// names nothing, so it is answered 404 with detail, as one that doesn't exist
// is, and ok is false.
func (rt *Router) PathID(w http.ResponseWriter, r *http.Request, name, detail string) (id uuid.UUID, ok bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		rt.Problem(w, r, http.StatusNotFound, detail)
		return uuid.Nil(), false
	}
	return id, true
}

// problemJSON is an RFC 9457 problem details document.
type problemJSON struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Problem answers with a problem no client needs to tell apart from others
// with its status: about:blank, titled with the status.
func (rt *Router) Problem(w http.ResponseWriter, r *http.Request, status int, detail string) {
	rt.TypedProblem(w, r, status, "about:blank", http.StatusText(status), detail)
}

// TypedProblem answers with a problem of its own type, whose title sums up the
// type rather than the status.
func (rt *Router) TypedProblem(w http.ResponseWriter, r *http.Request, status int, typ, title, detail string) {
	rt.Reply(w, r, status, "application/problem+json", problemJSON{Type: typ, Title: title, Status: status, Detail: detail})
}

// Reply answers with body as JSON.
func (rt *Router) Reply(w http.ResponseWriter, r *http.Request, status int, contentType string, body any) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		rt.log.WarnContext(r.Context(), "write reply", "err", err)
	}
}

// statusCapture records the status and headers a handler writes, discarding the body.
type statusCapture struct {
	header http.Header
	status int
}

func (c *statusCapture) Header() http.Header         { return c.header }
func (c *statusCapture) WriteHeader(status int)      { c.status = status }
func (c *statusCapture) Write(b []byte) (int, error) { return len(b), nil }
