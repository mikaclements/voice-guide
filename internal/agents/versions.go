package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"uuid"
)

// Version is one published version of an agent. A saved version never
// changes, so a session keeps exactly the agent it started with.
type Version struct {
	Agent
	Number int
}

var (
	// ErrNotFound reports that the workspace has no such agent or version.
	ErrNotFound = errors.New("no such agent version")
	// ErrAgentInAnotherWorkspace reports an agent ID that another workspace
	// already uses; IDs are unique across workspaces.
	ErrAgentInAnotherWorkspace = errors.New("the agent ID belongs to another workspace")
)

// Versions keeps every version of the workspaces' agents.
type Versions interface {
	// Newest returns the newest version of each of the workspace's agents, by name.
	Newest(ctx context.Context, workspace uuid.UUID) ([]Version, error)
	// Version returns one exact version, or ErrNotFound.
	Version(ctx context.Context, workspace, agent uuid.UUID, number int) (Version, error)
	// Save stores a as its next version unless the newest version has the same
	// hash, and returns the number that holds a's content and whether it is new.
	// Saves of one agent take turns, so no number is given twice.
	Save(ctx context.Context, workspace uuid.UUID, a Agent, hash string) (number int, saved bool, err error)
}

// Published reports what publishing did with one agent.
type Published struct {
	Agent  uuid.UUID
	Name   string
	Number int
	New    bool
}

// Publish saves each agent as its next version when its settings or persona
// differ from its newest version's. Every agent is checked before any is
// saved, so one broken agent file publishes nothing.
func Publish(ctx context.Context, store Versions, workspace uuid.UUID, defs []Agent) ([]Published, error) {
	if err := Check(defs); err != nil {
		return nil, err
	}
	out := make([]Published, 0, len(defs))
	for _, a := range defs {
		n, saved, err := store.Save(ctx, workspace, a.copy(), contentHash(a))
		if err != nil {
			return out, fmt.Errorf("publish %s (%s): %w", a.Name, a.ID, err)
		}
		out = append(out, Published{Agent: a.ID, Name: a.Name, Number: n, New: saved})
	}
	return out, nil
}

// contentHash fingerprints everything a version holds, so any change to the
// settings or the persona gives a different hash.
func contentHash(a Agent) string {
	reads := a.Reads
	if reads == nil {
		reads = []string{} // no context kinds hash alike however they were written
	}
	b, err := json.Marshal(struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Reads       []string `json:"reads"`
		// Left out when empty, so agents saved before it existed keep their hash
		// and a republish of an unchanged file adds no version.
		SessionContext []string `json:"session_context,omitempty"`
		Persona        string   `json:"persona"`
		// Left out when empty, for the same reason.
		Summary string `json:"summary,omitempty"`
		// Left out when the user opens, for the same reason.
		Opens bool `json:"opens,omitempty"`
		// Left out when empty, for the same reason.
		Model string `json:"model,omitempty"`
		// Left out when empty or 0, for the same reason.
		Voice string  `json:"voice,omitempty"`
		Speed float64 `json:"speed,omitempty"`
	}{a.ID.String(), a.Name, a.Description, reads, a.SessionContext, a.Persona, a.Summary, a.Opens, a.Model, a.Voice, a.Speed})
	if err != nil {
		panic(err) // strings, a slice of strings, a bool and a speed that Check keeps finite always encode
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
