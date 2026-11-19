package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/conversation"
)

type turnJSON struct {
	RequestID   uuid.UUID   `json:"request_id"`
	Words       string      `json:"words"`
	ScreenInput []blockJSON `json:"screen_input"`
	Opening     bool        `json:"opening"`
}

// turnEventJSON names the turn a done or lost event ends, and what answered it.
type turnEventJSON struct {
	TurnID string `json:"turn_id,omitempty"`
	Seq    int    `json:"seq,omitempty"`
	Model  string `json:"model,omitempty"`
}

// takeTurn answers the user's words in one of their sessions, streaming the
// reply back as server-sent events: text as it arrives, then done with the
// turn's ID and number. A turn refused before the stream starts is a problem
// with its status; once it has started the status is sent, so a failure ends
// it with a lost event instead. A reply the model doesn't finish always ends
// with lost, since its turn is saved lost, even before any text. The router
// answers a ticket on any other session's path 404 before this runs, so the
// ticket's session is the path's.
func (s *server) takeTurn(w http.ResponseWriter, r *http.Request, c access.Caller) {
	var in turnJSON
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, bodyLimit(s.svc.TurnLimit()))).Decode(&in)
	if tooLarge := new(http.MaxBytesError); errors.As(err, &tooLarge) {
		s.api.Problem(w, r, http.StatusRequestEntityTooLarge, "the body is over the limit for a turn")
		return
	}
	if err != nil || in.RequestID == uuid.Nil() {
		s.api.Problem(w, r, http.StatusBadRequest, "the body must be a JSON object with request_id, a UUID, the user's words, and an optional screen_input list of name and content")
		return
	}
	req := conversation.TurnRequest{Session: c.Session, RequestID: in.RequestID, Words: in.Words, Opening: in.Opening}
	for _, b := range in.ScreenInput {
		req.ScreenInput = append(req.ScreenInput, conversation.ScreenInputBlock(b))
	}

	stream := eventStream{w: w}
	turn, err := s.svc.TakeTurn(r.Context(), c, req, func(text string) {
		stream.send("text", struct {
			Text string `json:"text"`
		}{text})
	})
	var failed *conversation.ReplyFailedError
	var unusable *conversation.ScreenInputError
	switch {
	case err == nil:
		stream.send("done", turnEventJSON{TurnID: turn.ID.String(), Seq: turn.Seq, Model: turn.Model()})
	case errors.As(err, &failed):
		s.log.WarnContext(r.Context(), "the model's reply failed", "kind", failed.Kind)
		stream.send("lost", turnEventJSON{TurnID: turn.ID.String(), Seq: turn.Seq, Model: turn.Model()})
	case stream.started:
		s.log.ErrorContext(r.Context(), "take a turn", "err", err)
		stream.send("lost", turnEventJSON{})
	case errors.Is(err, conversation.ErrNoSuchSession):
		s.api.Problem(w, r, http.StatusNotFound, "no such session")
	case errors.Is(err, conversation.ErrSessionEnded):
		s.api.SessionEnded(w, r)
	case errors.Is(err, conversation.ErrTurnInFlight):
		s.api.Problem(w, r, http.StatusConflict, "the session is answering another turn")
	case errors.Is(err, conversation.ErrTurnRepeated):
		s.api.TypedProblem(w, r, http.StatusConflict, turnRepeated, "Turn already taken",
			"this request_id already took a turn in this session; a new turn needs a new request_id")
	case errors.Is(err, conversation.ErrOpeningRefused):
		s.api.TypedProblem(w, r, http.StatusConflict, openingRefused, "Opening refused",
			"the session already has a turn, or its agent waits for the user, so the agent doesn't open it")
	case errors.As(err, &unusable), errors.Is(err, conversation.ErrWordsUnusable):
		s.api.Problem(w, r, http.StatusBadRequest, err.Error())
	case errors.Is(err, conversation.ErrScreenInputTooLarge), errors.Is(err, conversation.ErrWordsTooLong):
		s.api.Problem(w, r, http.StatusRequestEntityTooLarge, err.Error())
	default:
		s.log.ErrorContext(r.Context(), "take a turn", "err", err)
		s.api.Problem(w, r, http.StatusInternalServerError, "")
	}
	// The caller has the whole reply by now; the stage times follow it, off its path.
	if err := s.svc.RecordTiming(r.Context(), turn); err != nil {
		s.log.WarnContext(r.Context(), "record a turn's stage times", "err", err)
	}
}

// eventStream writes server-sent events, each flushed at once so the user
// sees the reply as it arrives. The first event starts the stream's 200.
type eventStream struct {
	w       http.ResponseWriter
	started bool
}

func (e *eventStream) send(event string, data any) {
	if !e.started {
		e.w.Header().Set("Content-Type", "text/event-stream")
		e.w.Header().Set("Cache-Control", "no-store")
		// A proxy in front, nginx say, would otherwise hold the reply back
		// until it has a buffer's worth.
		e.w.Header().Set("X-Accel-Buffering", "no")
		e.w.WriteHeader(http.StatusOK)
		e.started = true
	}
	// The data is JSON, so a line break in the reply can't end an event early.
	// A write fails only once the caller has gone, which cancels the request's
	// context and with it the turn; no one is left to tell.
	b, _ := json.Marshal(data)
	if _, err := fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", event, b); err == nil {
		_ = http.NewResponseController(e.w).Flush()
	}
}
