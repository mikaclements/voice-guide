// Package http is speech's part of the public HTTP API: setting up and ending
// a session's audio connection, the way WHIP (RFC 9725) does it. The browser
// posts its SDP offer and gets the answer back, 201 with the connection's own
// address; DELETE there ends that connection and no other. Its routes are
// registered on the API's router, behind the ticket check.
package http

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/api"
	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/speech"
)

// offerLimit bounds an offer's size, so a caller can't make the server read
// more. A browser's offer for audio and a data channel is 2 to 6 KB (one line
// per codec and per address), so 64 KiB leaves room for many addresses.
const offerLimit = 64 << 10

// retryAfter is how many seconds a full server asks a browser to wait before
// offering again: a round guess, long enough not to hammer a full server and
// short enough for someone waiting; a connection that never connects frees
// its place within about 30s, and behind a load balancer the retry may reach
// another server at once.
const retryAfter = "5"

type handler struct {
	svc *speech.Service
	api *api.Router
	log *slog.Logger
}

// Register adds speech's routes to the API. Both are in one session: the
// router answers a ticket on any other session's path 404 before they run, so
// the ticket's session is the path's.
func Register(rt *api.Router, svc *speech.Service) {
	h := &handler{svc: svc, api: rt, log: rt.Log()}
	rt.Handle("POST /v1/sessions/{id}/audio", api.RequestTimeout, h.connect)
	rt.Handle("DELETE /v1/sessions/{id}/audio/{conn}", api.RequestTimeout, h.hangup)
}

func (h *handler) connect(w http.ResponseWriter, r *http.Request, c access.Caller) {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/sdp" {
		h.api.Problem(w, r, http.StatusUnsupportedMediaType, "the body must be an SDP offer, sent as application/sdp")
		return
	}
	offer, err := io.ReadAll(http.MaxBytesReader(w, r.Body, offerLimit))
	if tooLarge := new(http.MaxBytesError); errors.As(err, &tooLarge) {
		h.api.Problem(w, r, http.StatusRequestEntityTooLarge, "the offer is over the size limit")
		return
	}
	if err != nil {
		h.api.Problem(w, r, http.StatusBadRequest, "the offer couldn't be read")
		return
	}
	id, answer, err := h.svc.Connect(r.Context(), c, c.Session, string(offer))
	if err != nil {
		h.fail(w, r, err, "set up an audio connection")
		return
	}
	w.Header().Set("Location", r.URL.Path+"/"+id.String())
	w.Header().Set("Content-Type", "application/sdp")
	w.WriteHeader(http.StatusCreated)
	if _, err := io.WriteString(w, answer); err != nil {
		h.log.WarnContext(r.Context(), "write the answer", "err", err)
	}
}

func (h *handler) hangup(w http.ResponseWriter, r *http.Request, c access.Caller) {
	id, ok := h.api.PathID(w, r, "conn", speech.ErrNoConnection.Error())
	if !ok {
		return
	}
	if err := h.svc.Hangup(r.Context(), c, c.Session, id); err != nil {
		h.fail(w, r, err, "end an audio connection")
		return
	}
	w.WriteHeader(http.StatusOK) // as WHIP answers an ended session
}

// fail answers a refusal from speech's service, or a failure.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error, doing string) {
	switch {
	case errors.Is(err, speech.ErrTicketExpired):
		h.api.TicketExpired(w, r)
	case errors.Is(err, conversation.ErrNoSuchSession):
		h.api.Problem(w, r, http.StatusNotFound, "no such session")
	case errors.Is(err, conversation.ErrSessionEnded):
		// The type conversation's turns answer with, so a client handles both alike.
		h.api.SessionEnded(w, r)
	case errors.Is(err, speech.ErrNoConnection):
		h.api.Problem(w, r, http.StatusNotFound, err.Error())
	case errors.Is(err, speech.ErrBadOffer):
		h.api.Problem(w, r, http.StatusBadRequest, err.Error())
	case errors.Is(err, speech.ErrFull):
		w.Header().Set("Retry-After", retryAfter)
		h.api.Problem(w, r, http.StatusServiceUnavailable, err.Error()+"; send the offer again")
	case errors.Is(err, speech.ErrStopping):
		h.api.Problem(w, r, http.StatusServiceUnavailable, "this server is stopping; send the offer again")
	case r.Context().Err() != nil:
		// The server is stopping or the deadline passed: no fault here, and a
		// retry may reach another server.
		h.log.WarnContext(r.Context(), doing+": cut short", "err", err)
		h.api.Problem(w, r, http.StatusServiceUnavailable, "the request was cut short; send it again")
	default:
		h.log.ErrorContext(r.Context(), doing, "err", err)
		h.api.Problem(w, r, http.StatusInternalServerError, "")
	}
}
