// Package http is the agents module's part of the public HTTP API: publishing
// an agent and reading its newest version, for whoever publishes the
// workspace's agents, registered on the API's router.
package http

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/agents/agentfile"
	"github.com/mikaclements/voice-guide/internal/api"
)

type server struct {
	catalog   *agents.Catalog
	publisher *agents.Publisher
	api       *api.Router
	log       *slog.Logger
}

// publishTimeout covers the voice check, which may use all of its time, and then the save.
const publishTimeout = agents.VoicesTimeout + api.RequestTimeout

// maxFileBytes bounds an agent file's body: a round guess, since the largest real agent file is
// 11 KB, so 256 KiB leaves room to grow while refusing a body that is no agent file. To be
// revisited when a file presses against it.
const maxFileBytes = 256 << 10

// unknownVoice is the type of the problem for a voice the text-to-speech service doesn't offer,
// as a session's start answers it.
const unknownVoice = "/v1/problems/unknown-voice"

// Register adds the agents module's routes to the API.
func Register(rt *api.Router, catalog *agents.Catalog, publisher *agents.Publisher) {
	s := &server{catalog: catalog, publisher: publisher, api: rt, log: rt.Log()}
	rt.HandleProduct("GET /v1/agents/{id}", api.RequestTimeout, access.PublishAgents, s.readAgent)
	rt.HandleProduct("PUT /v1/agents/{id}", publishTimeout, access.PublishAgents, s.publishAgent)
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

// publishedJSON is the version a publish answers with, and whether its voice was checked.
type publishedJSON struct {
	versionJSON
	VoiceChecked bool `json:"voice_checked"`
}

// publishAgent publishes the agent file in the body as the agent the path names, through the same
// checked publish as the publish script. The caller wrote the file, so a refusal says what is wrong
// with it in the loader's or the check's words.
func (s *server) publishAgent(w http.ResponseWriter, r *http.Request, c access.Caller) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.api.Problem(w, r, http.StatusUnprocessableEntity, "the agent ID in the path isn't a UUID")
		return
	}
	// RFC 7763 names text/markdown; a media type's parameters, a charset, don't change it.
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "text/markdown" {
		s.api.Problem(w, r, http.StatusUnsupportedMediaType, "send the agent file with Content-Type: text/markdown")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFileBytes))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		s.api.Problem(w, r, http.StatusRequestEntityTooLarge, "the agent file is over 256 KiB")
		return
	case err != nil:
		s.api.Problem(w, r, http.StatusBadRequest, "the agent file couldn't be read; send it again")
		return
	case len(data) == 0:
		s.api.Problem(w, r, http.StatusUnprocessableEntity, "the body is empty: send the agent file")
		return
	case !utf8.Valid(data):
		s.api.Problem(w, r, http.StatusUnprocessableEntity, "the agent file isn't UTF-8")
		return
	}
	a, err := agentfile.Parse("the agent file", data)
	switch {
	case err != nil:
		s.api.Problem(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	case a.ID != id:
		s.api.Problem(w, r, http.StatusUnprocessableEntity, "the agent file's id "+a.ID.String()+" isn't the path's "+id.String())
		return
	}
	res, err := s.publisher.Publish(r.Context(), c.Workspace, []agents.Agent{a})
	var refusal agents.Refusal
	switch {
	case errors.Is(err, agents.ErrUnknownVoice):
		s.api.TypedProblem(w, r, http.StatusUnprocessableEntity, unknownVoice, "Unknown voice", err.Error())
		return
	case errors.As(err, &refusal):
		s.api.Problem(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	case errors.Is(err, agents.ErrAgentInAnotherWorkspace):
		s.api.Problem(w, r, http.StatusConflict, "the agent ID belongs to another workspace; pick a new ID")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "publish an agent", "err", err)
		s.api.Problem(w, r, http.StatusServiceUnavailable, "the agent couldn't be saved; retry")
		return
	}
	if res.VoicesNotChecked != nil {
		s.log.WarnContext(r.Context(), "an agent's voice was published unchecked", "err", res.VoicesNotChecked)
	}
	// The version the publish saved or found, read by its own number, so a publish made a moment
	// later by someone else isn't answered as this one.
	p := res.Published[0]
	v, err := s.catalog.Version(r.Context(), c.Workspace, p.Agent, p.Number)
	if err != nil {
		s.log.ErrorContext(r.Context(), "read a published agent", "err", err)
		s.api.Problem(w, r, http.StatusServiceUnavailable, "the agent was saved but couldn't be read back; read it with GET")
		return
	}
	status := http.StatusOK
	if p.New {
		status = http.StatusCreated
	}
	s.api.Reply(w, r, status, "application/json", publishedJSON{newVersionJSON(v), res.VoicesNotChecked == nil})
}
