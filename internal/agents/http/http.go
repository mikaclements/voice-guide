// Package http is the agents module's part of the public HTTP API: an agent's
// newest version, for whoever publishes the workspace's agents, registered on
// the API's router.
package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/api"
)

type server struct {
	catalog *agents.Catalog
	api     *api.Router
	log     *slog.Logger
}

// Register adds the agents module's routes to the API.
func Register(rt *api.Router, catalog *agents.Catalog) {
	s := &server{catalog: catalog, api: rt, log: rt.Log()}
	rt.HandleProduct("GET /v1/agents/{id}", api.RequestTimeout, access.PublishAgents, s.readAgent)
}

// versionJSON is an agent's version as it is kept, with its number and when
// it was published: an unset setting is empty, or 0, and no summary schema is
// null.
type versionJSON struct {
	ID               string          `json:"id"`
	Version          int             `json:"version"`
	PublishedAt      time.Time       `json:"published_at"`
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	Reads            []string        `json:"reads"`
	SessionContext   []string        `json:"session_context"`
	Persona          string          `json:"persona"`
	Summary          string          `json:"summary"`
	SummaryModel     string          `json:"summary_model"`
	SummarySchema    json.RawMessage `json:"summary_schema"`
	SummaryDocuments []string        `json:"summary_documents"`
	Opens            bool            `json:"opens"`
	Model            string          `json:"model"`
	Voice            string          `json:"voice"`
	Speed            float64         `json:"speed"`
}

// readAgent answers the newest version of one of the caller's agents. Another
// workspace's agent is answered as one never published, so a key learns
// nothing of other workspaces' agents.
func (s *server) readAgent(w http.ResponseWriter, r *http.Request, c access.Caller) {
	id, ok := s.api.PathID(w, r, "id", "no such agent")
	if !ok {
		return
	}
	v, err := s.catalog.Newest(r.Context(), c.Workspace, id)
	switch {
	case errors.Is(err, agents.ErrNotFound):
		s.api.Problem(w, r, http.StatusNotFound, "no such agent")
		return
	case err != nil:
		// The caller learns only to retry; the reason is for the log.
		s.log.ErrorContext(r.Context(), "read an agent", "err", err)
		s.api.Problem(w, r, http.StatusServiceUnavailable, "the agent couldn't be read; retry")
		return
	}
	s.api.Reply(w, r, http.StatusOK, "application/json", versionJSON{
		ID: v.ID.String(), Version: v.Number, PublishedAt: v.PublishedAt, Name: v.Name, Description: v.Description,
		Reads: append([]string{}, v.Reads...), SessionContext: append([]string{}, v.SessionContext...),
		Persona: v.Persona, Summary: v.Summary, SummaryModel: v.SummaryModel, SummarySchema: v.SummarySchema,
		SummaryDocuments: append([]string{}, v.SummaryDocuments...),
		Opens:            v.Opens, Model: v.Model, Voice: v.Voice, Speed: v.Speed,
	})
}
