// Package fake keeps agent versions in memory: the agents module's fake for
// the core's tests and for load tests.
package fake

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/agents"
)

// Versions keeps agent versions in memory. It is safe for concurrent use.
type Versions struct {
	mu     sync.Mutex
	agents map[uuid.UUID]*history
}

type history struct {
	workspace uuid.UUID
	versions  []agents.Version // versions[i] is number i+1
	hashes    []string
}

// New returns an empty store.
func New() *Versions {
	return &Versions{agents: make(map[uuid.UUID]*history)}
}

// Newest returns the newest version of each of the workspace's agents, by name.
func (f *Versions) Newest(_ context.Context, workspace uuid.UUID) ([]agents.Version, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []agents.Version
	for _, h := range f.agents {
		if h.workspace == workspace {
			out = append(out, clone(h.versions[len(h.versions)-1]))
		}
	}
	slices.SortFunc(out, func(a, b agents.Version) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID.String(), b.ID.String()))
	})
	return out, nil
}

// Version returns one exact version, or agents.ErrNotFound.
func (f *Versions) Version(_ context.Context, workspace, agent uuid.UUID, number int) (agents.Version, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.agents[agent]
	if !ok || h.workspace != workspace || number < 1 || number > len(h.versions) {
		return agents.Version{}, agents.ErrNotFound
	}
	return clone(h.versions[number-1]), nil
}

// Save stores a as its next version unless the newest version has the same hash.
func (f *Versions) Save(_ context.Context, workspace uuid.UUID, a agents.Agent, hash string) (int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.agents[a.ID]
	switch {
	case !ok:
		h = &history{workspace: workspace}
		f.agents[a.ID] = h
	case h.workspace != workspace:
		return 0, false, agents.ErrAgentInAnotherWorkspace
	case h.hashes[len(h.hashes)-1] == hash:
		return len(h.versions), false, nil
	}
	n := len(h.versions) + 1
	h.versions = append(h.versions, clone(agents.Version{Agent: a, Number: n}))
	h.hashes = append(h.hashes, hash)
	return n, true, nil
}

// clone gives each caller its own context kinds, so no one can change a saved version.
func clone(v agents.Version) agents.Version {
	v.Reads = slices.Clone(v.Reads)
	v.SessionContext = slices.Clone(v.SessionContext)
	v.Grading.Levels = slices.Clone(v.Grading.Levels)
	v.Grading.Criteria = slices.Clone(v.Grading.Criteria)
	return v
}
