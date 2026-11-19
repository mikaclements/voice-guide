// Package http is conversation's part of the public HTTP API: agents,
// sessions and typed turns, registered on the API's router.
package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/api"
	"github.com/mikaclements/voice-guide/internal/conversation"
)

type server struct {
	svc *conversation.Service
	api *api.Router
	log *slog.Logger
}

// Register adds conversation's routes to the API.
func Register(rt *api.Router, svc *conversation.Service) {
	s := &server{svc: svc, api: rt, log: rt.Log()}
	rt.HandleProduct("GET /v1/agents", api.RequestTimeout, s.listAgents)
	rt.HandleProduct("GET /v1/voices", api.RequestTimeout, s.listVoices)
	rt.HandleProduct("POST /v1/sessions", api.RequestTimeout, s.startSession)
	rt.HandleTicket("POST /v1/sessions/{id}/turns", svc.TurnDeadline(), s.takeTurn)
	rt.HandleProduct("GET /v1/users/{user}/sessions", api.RequestTimeout, s.listPastSessions)
	rt.HandleProduct("GET /v1/sessions/{id}", api.RequestTimeout, s.readPastSession)
	rt.HandleSessionEnd("POST /v1/sessions/{id}/end", api.RequestTimeout, s.endSession)
}

// listVoices answers the voices a session may speak in, as the text-to-speech
// service names them.
func (s *server) listVoices(w http.ResponseWriter, r *http.Request, _ access.Caller) {
	voices, err := s.svc.Voices(r.Context())
	if err != nil {
		s.log.ErrorContext(r.Context(), "list voices", "err", err)
		s.api.Problem(w, r, http.StatusServiceUnavailable, "the voices couldn't be listed; retry")
		return
	}
	s.api.Reply(w, r, http.StatusOK, "application/json", struct {
		Voices []string `json:"voices"`
	}{Voices: append([]string{}, voices...)})
}

type agentJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// What a product must send when it starts a session with this agent, by name.
	SessionContext []string `json:"session_context"`
	// What it may also send that only the summary reads, by name.
	SummaryDocuments []string `json:"summary_documents"`
}

func (s *server) listAgents(w http.ResponseWriter, r *http.Request, c access.Caller) {
	list, err := s.svc.Agents(r.Context(), c)
	if err != nil {
		// The caller learns only that it failed; the reason is for the log.
		s.log.ErrorContext(r.Context(), "list agents", "err", err)
		s.api.Problem(w, r, http.StatusInternalServerError, "")
		return
	}
	body := struct {
		Agents []agentJSON `json:"agents"`
	}{Agents: make([]agentJSON, 0, len(list))}
	for _, a := range list {
		body.Agents = append(body.Agents, agentJSON{
			ID: a.ID.String(), Name: a.Name, Description: a.Description, SessionContext: append([]string{}, a.SessionContext...),
			SummaryDocuments: append([]string{}, a.SummaryDocuments...),
		})
	}
	s.api.Reply(w, r, http.StatusOK, "application/json", body)
}

// bodyLimit bounds a request's body, so a caller can't make the server read
// more, while always fitting the request's limit in characters: a character
// outside the basic plane escaped in JSON takes 12 bytes, plus room for the
// rest of the request. The character limit itself is checked once the body is
// read.
func bodyLimit(chars int) int64 {
	return 12*int64(chars) + 64<<10
}

type startJSON struct {
	AgentID        uuid.UUID   `json:"agent_id"`
	RequestID      uuid.UUID   `json:"request_id"`
	SessionContext []blockJSON `json:"session_context"`
	// The product's own name for the user, sent by its server with its key.
	User string `json:"user"`
	// The voice and speed replies are spoken in, overriding the agent's; a speed of 1 is the
	// voice's own pace.
	Voice string   `json:"voice"`
	Speed *float64 `json:"speed"`
}

// blockJSON is a named block of text: session context or screen input.
type blockJSON struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type sessionJSON struct {
	ID           string    `json:"id"`
	AgentID      string    `json:"agent_id"`
	AgentVersion int       `json:"agent_version"`
	StartedAt    time.Time `json:"started_at"`
	// What every reply in the session is spoken in; a repeat of a start with another voice sees
	// here that it got the first start's.
	Voice string  `json:"voice"`
	Speed float64 `json:"speed"`
	// Left out while the session is live.
	EndedAt *time.Time `json:"ended_at,omitempty"`
}

// startedJSON is a start's reply: the session, and the ticket the product's
// server hands the user's browser for it.
type startedJSON struct {
	sessionJSON
	// Left out once the session's tickets have expired: the product starts a
	// new session then.
	Ticket string `json:"ticket,omitempty"`
}

func (s *server) sessionReply(session conversation.Session) sessionJSON {
	voice, speed := s.svc.Speaking(session)
	out := sessionJSON{ID: session.ID.String(), AgentID: session.Agent.String(), AgentVersion: session.AgentVersion, StartedAt: session.StartedAt.UTC(), Voice: voice, Speed: speed}
	if session.Ended() {
		ended := session.EndedAt.UTC()
		out.EndedAt = &ended
	}
	return out
}

// startSession starts a session, or returns the one the same request ID
// started: 201 for a new session, 200 for a repeat, so a client that retries
// after a lost reply gets the same answer. Each reply carries a fresh ticket
// for the session, issued then, which expires when the session's first did.
func (s *server) startSession(w http.ResponseWriter, r *http.Request, c access.Caller) {
	var in startJSON
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, bodyLimit(s.svc.SessionContextLimit()))).Decode(&in)
	if tooLarge := new(http.MaxBytesError); errors.As(err, &tooLarge) {
		s.api.Problem(w, r, http.StatusRequestEntityTooLarge, "the body is over the limit for starting a session")
		return
	}
	if err != nil || in.AgentID == uuid.Nil() || in.RequestID == uuid.Nil() {
		s.api.Problem(w, r, http.StatusBadRequest, "the body must be a JSON object with agent_id and request_id, each a UUID, and optional session_context, a list of name and content, voice, a string, and speed, a number")
		return
	}
	req := conversation.StartRequest{Agent: in.AgentID, RequestID: in.RequestID, User: in.User, Voice: in.Voice, Speed: in.Speed}
	for _, b := range in.SessionContext {
		req.SessionContext = append(req.SessionContext, conversation.SessionContextBlock(b))
	}
	started, isNew, err := s.svc.StartSession(r.Context(), c, req)
	var mismatch *conversation.SessionContextMismatchError
	switch {
	case errors.Is(err, conversation.ErrUserReference):
		s.api.Problem(w, r, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, conversation.ErrVoiceSettings):
		s.api.TypedProblem(w, r, http.StatusUnprocessableEntity, voiceSettings, "Voice or speed refused", err.Error())
		return
	case errors.Is(err, conversation.ErrUnknownVoice):
		s.api.TypedProblem(w, r, http.StatusUnprocessableEntity, unknownVoice, "Unknown voice", err.Error())
		return
	case errors.Is(err, conversation.ErrVoicesUnavailable):
		// 503, so the product retries the same request; the reason stays in the log.
		s.log.ErrorContext(r.Context(), "start a session", "err", err)
		s.api.Problem(w, r, http.StatusServiceUnavailable, "the voices couldn't be listed to check the voice; retry the same request")
		return
	case errors.Is(err, conversation.ErrNoSuchAgent):
		s.api.Problem(w, r, http.StatusNotFound, "no such agent")
		return
	case errors.As(err, &mismatch):
		s.api.TypedProblem(w, r, http.StatusUnprocessableEntity, sessionContextMismatch, "Session context doesn't match the agent", mismatch.Error())
		return
	case errors.Is(err, conversation.ErrSessionContextTooLarge):
		s.api.Problem(w, r, http.StatusRequestEntityTooLarge, err.Error())
		return
	case errors.Is(err, conversation.ErrRequestReused):
		// 422: the request can never succeed with this ID, so retrying it is pointless;
		// 409 stays for a retry that arrives while the first is still in flight.
		s.api.TypedProblem(w, r, http.StatusUnprocessableEntity, requestReused, "Request ID reused for a different request", err.Error())
		return
	case errors.Is(err, conversation.ErrUserContextUnavailable):
		// 503, so the product retries the same request; the reason stays in the log.
		s.log.ErrorContext(r.Context(), "start a session", "err", err)
		s.api.Problem(w, r, http.StatusServiceUnavailable, "user context is unavailable; retry the same request")
		return
	case errors.Is(err, conversation.ErrUserUnavailable):
		s.log.ErrorContext(r.Context(), "start a session", "err", err)
		s.api.Problem(w, r, http.StatusServiceUnavailable, "the user couldn't be looked up; retry the same request")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "start a session", "err", err)
		s.api.Problem(w, r, http.StatusInternalServerError, "")
		return
	}
	ticket, ok, err := s.api.Ticket(access.Caller{Workspace: started.Workspace, User: started.User, Session: started.ID}, started.StartedAt)
	if err != nil {
		// The session is saved; the same request gets it back with a ticket.
		s.log.ErrorContext(r.Context(), "mint a ticket", "err", err)
		s.api.Problem(w, r, http.StatusInternalServerError, "")
		return
	}
	reply := startedJSON{sessionJSON: s.sessionReply(started)}
	if ok {
		reply.Ticket = ticket.Reveal()
	}
	status := http.StatusOK
	if isNew {
		status = http.StatusCreated
	}
	// The reply carries a credential, so no cache along the way may keep it,
	// as for OAuth's token replies (RFC 6749).
	w.Header().Set("Cache-Control", "no-store")
	s.api.Reply(w, r, status, "application/json", reply)
}

// endSession ends a session and answers 200 with it, ended; ending it again
// answers the same, so a client that retries after a lost reply gets the same
// answer. The browser ends its session with its ticket, the product's server
// any session of its workspace with its key. The session stays, to be read
// and resumed.
func (s *server) endSession(w http.ResponseWriter, r *http.Request, c access.Caller) {
	id, ok := s.api.PathID(w, r, "id", "no such session")
	if !ok {
		return
	}
	ended, err := s.svc.EndSession(r.Context(), c, id)
	switch {
	case errors.Is(err, conversation.ErrNoSuchSession):
		s.api.Problem(w, r, http.StatusNotFound, "no such session")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "end a session", "err", err)
		s.api.Problem(w, r, http.StatusInternalServerError, "")
		return
	}
	s.api.Reply(w, r, http.StatusOK, "application/json", s.sessionReply(ended))
}

// The problem types, for errors a client must tell apart from another with
// the same status. A client compares the type as sent; resolved against the
// API's address, each names where its documentation is to be served.
const (
	sessionContextMismatch = "/v1/problems/session-context-mismatch"
	requestReused          = "/v1/problems/request-reused"
	voiceSettings          = "/v1/problems/voice-settings"
	unknownVoice           = "/v1/problems/unknown-voice"
	// A repeat of a turn, told apart from the 409 for another turn in flight, which a client
	// may send again once that turn ends.
	turnRepeated = "/v1/problems/turn-repeated"
	// An opening the session doesn't take, told apart from the 409 for another turn in flight:
	// the agent doesn't open, and the client lets it go without a word to the user.
	openingRefused = "/v1/problems/opening-refused"
)
