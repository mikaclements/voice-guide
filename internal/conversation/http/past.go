package http

import (
	"errors"
	"net/http"
	"strconv"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/conversation"
)

// pastSessionJSON is a session as its product reads it later, in the shape
// of a delivery's session: the user by the product's own reference, never
// voice-guide's user ID. A session still live has no end and no summary, both
// null.
type pastSessionJSON struct {
	ID           uuid.UUID  `json:"id"`
	User         string     `json:"user"`
	AgentID      uuid.UUID  `json:"agent_id"`
	AgentVersion int        `json:"agent_version"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at"`
	Summary      *string    `json:"summary"`
}

func pastReply(p conversation.PastSession) pastSessionJSON {
	out := pastSessionJSON{
		ID: p.Session.ID, User: p.Reference, AgentID: p.Session.Agent, AgentVersion: p.Session.AgentVersion,
		StartedAt: p.Session.StartedAt.UTC(), Summary: p.Summary,
	}
	if p.Session.Ended() {
		ended := p.Session.EndedAt.UTC()
		out.EndedAt = &ended
	}
	return out
}

// heardTurnJSON is one turn as the user heard it: their words, and the reply
// as far as they heard it, cut when they interrupted it.
type heardTurnJSON struct {
	Seq   int    `json:"seq"`
	Words string `json:"words"`
	Reply string `json:"reply"`
	Cut   bool   `json:"cut"`
}

// readJSON is one session read whole: the session, then every turn it took.
type readJSON struct {
	pastSessionJSON
	Turns []heardTurnJSON `json:"turns"`
}

// listPastSessions answers with a page of the user's sessions, newest first,
// without their turns. The user is the product's own reference, which the
// product never has to translate; one it never started a session for has an
// empty list. The next page is the one before the last session's ID.
func (s *server) listPastSessions(w http.ResponseWriter, r *http.Request, c access.Caller) {
	reference := r.PathValue("user")
	var before uuid.UUID
	if v := r.URL.Query().Get("before"); v != "" {
		var err error
		if before, err = uuid.Parse(v); err != nil {
			s.api.Problem(w, r, http.StatusBadRequest, "before must be a session ID")
			return
		}
	}
	limit := conversation.PastDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		var err error
		if limit, err = strconv.Atoi(v); err != nil {
			s.api.Problem(w, r, http.StatusBadRequest, "limit must be a number from 1 to "+strconv.Itoa(conversation.PastMaxLimit))
			return
		}
	}
	list, err := s.svc.PastSessions(r.Context(), c, reference, before, limit)
	switch {
	case errors.Is(err, conversation.ErrUserReference):
		// The same rule a start applies, so a reference no start could have made is a bad request, not a miss.
		s.api.Problem(w, r, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, conversation.ErrPastLimit):
		s.api.Problem(w, r, http.StatusBadRequest, "limit must be a number from 1 to "+strconv.Itoa(conversation.PastMaxLimit))
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "list a user's sessions", "err", err)
		s.api.Problem(w, r, http.StatusInternalServerError, "")
		return
	}
	body := struct {
		Sessions []pastSessionJSON `json:"sessions"`
	}{Sessions: make([]pastSessionJSON, 0, len(list))}
	for _, p := range list {
		body.Sessions = append(body.Sessions, pastReply(p))
	}
	s.api.Reply(w, r, http.StatusOK, "application/json", body)
}

// readPastSession answers with one session of the product's workspace and
// all its turns as the user heard them. Unlike a delivery, its length isn't
// capped: the product asks for it when it wants it.
func (s *server) readPastSession(w http.ResponseWriter, r *http.Request, c access.Caller) {
	id, ok := s.api.PathID(w, r, "id", "no such session")
	if !ok {
		return
	}
	past, turns, err := s.svc.PastSession(r.Context(), c, id)
	switch {
	case errors.Is(err, conversation.ErrNoSuchSession):
		s.api.Problem(w, r, http.StatusNotFound, "no such session")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "read a session", "err", err)
		s.api.Problem(w, r, http.StatusInternalServerError, "")
		return
	}
	body := readJSON{pastSessionJSON: pastReply(past), Turns: make([]heardTurnJSON, 0, len(turns))}
	for _, t := range turns {
		body.Turns = append(body.Turns, heardTurnJSON{Seq: t.Seq, Words: t.Words, Reply: t.HeardReply(), Cut: t.Status == conversation.TurnCut})
	}
	s.api.Reply(w, r, http.StatusOK, "application/json", body)
}
