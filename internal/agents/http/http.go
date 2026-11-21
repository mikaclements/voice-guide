// Package http is the agents module's part of the public HTTP API, the routes
// on one agent, registered on the API's router.
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

// versionJSON is an agent's version under the names and in the shapes of its
// agent file's settings block, so a product reads back what it wrote, with
// the version's number and when it was published. A setting the file leaves
// out is null, and a list it leaves out is empty. Every route that answers
// with a version answers this.
type versionJSON struct {
	ID             string       `json:"id"`
	Version        int          `json:"version"`
	PublishedAt    time.Time    `json:"published_at"`
	Name           string       `json:"name"`
	Description    string       `json:"description"`
	Reads          []string     `json:"reads"`
	SessionContext []string     `json:"session_context"`
	Summary        *summaryJSON `json:"summary"`
	Opens          string       `json:"opens"`
	Model          *string      `json:"model"`
	Voice          *string      `json:"voice"`
	Speed          *float64     `json:"speed"`
	Persona        string       `json:"persona"`
}

// summaryJSON is the summary section in the file's mapping form.
type summaryJSON struct {
	Instruction string          `json:"instruction"`
	Model       *string         `json:"model"`
	Schema      json.RawMessage `json:"schema"`
	Documents   []string        `json:"documents"`
}

// newVersionJSON returns v as a route answers it.
func newVersionJSON(v agents.Version) versionJSON {
	out := versionJSON{
		ID: v.ID.String(), Version: v.Number, PublishedAt: v.PublishedAt.UTC(), Name: v.Name, Description: v.Description,
		Reads: append([]string{}, v.Reads...), SessionContext: append([]string{}, v.SessionContext...),
		Opens: "user", Model: unlessEmpty(v.Model), Voice: unlessEmpty(v.Voice), Persona: v.Persona,
	}
	if v.Opens {
		out.Opens = "agent"
	}
	if v.Speed != 0 {
		out.Speed = &v.Speed
	}
	if v.Summary != "" {
		out.Summary = &summaryJSON{
			Instruction: v.Summary, Model: unlessEmpty(v.SummaryModel), Schema: v.SummarySchema,
			Documents: append([]string{}, v.SummaryDocuments...),
		}
	}
	return out
}

// unlessEmpty returns s, or nil for a setting left unset.
func unlessEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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
	s.api.Reply(w, r, http.StatusOK, "application/json", newVersionJSON(v))
}
