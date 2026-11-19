package agents

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"uuid"
)

// Agent is an agent's definition: who it is and what it may know about a user.
type Agent struct {
	ID          uuid.UUID
	Name        string
	Description string
	// Reads lists the context kinds the agent may be given about a user.
	Reads []string
	// SessionContext names what the product must send when a session starts,
	// such as the user's resume: the product already holds it, so voice-guide
	// doesn't keep a copy of its own.
	SessionContext []string
	Persona        string
	// Summary is what to note about a session once it ends, for the
	// agent's next session with the user; empty, no summary is written.
	Summary string
	// Opens says the agent speaks first in its sessions, rather than waiting
	// for the user.
	Opens bool
	// Model names the model that answers this agent's turns and writes its
	// summaries; empty, the server's own model answers.
	Model string
}

// Catalog is how the other modules see agents: the newest versions a
// workspace offers, and any exact version a session started on.
type Catalog struct {
	store Versions

	mu    sync.Mutex
	exact map[versionKey]Version
}

type versionKey struct {
	workspace, agent uuid.UUID
	number           int
}

// NewCatalog returns the catalog over the store of agent versions.
func NewCatalog(store Versions) *Catalog {
	return &Catalog{store: store, exact: make(map[versionKey]Version)}
}

// Available returns the newest version of each of the workspace's agents, by
// name. It is read from the store on each call, so a publish shows without a
// restart and every instance offers the same versions.
func (c *Catalog) Available(ctx context.Context, workspace uuid.UUID) ([]Version, error) {
	list, err := c.store.Newest(ctx, workspace)
	if err != nil {
		return nil, fmt.Errorf("read the newest agent versions: %w", err)
	}
	for i := range list {
		list[i].Agent = list[i].copy()
	}
	return list, nil
}

// Version returns one exact version, or ErrNotFound. A saved version never
// changes, so it is read from the store once and then served from memory.
func (c *Catalog) Version(ctx context.Context, workspace, agent uuid.UUID, number int) (Version, error) {
	key := versionKey{workspace, agent, number}
	c.mu.Lock()
	v, ok := c.exact[key]
	c.mu.Unlock()
	if !ok {
		var err error
		if v, err = c.store.Version(ctx, workspace, agent, number); err != nil {
			return Version{}, err
		}
		c.mu.Lock()
		c.exact[key] = v
		c.mu.Unlock()
	}
	v.Agent = v.copy()
	return v, nil
}

// Check refuses an invalid agent, or one ID used by two agents: what
// publishing checks before saving anything.
func Check(defs []Agent) error {
	seen := make(map[uuid.UUID]bool, len(defs))
	for _, a := range defs {
		if err := validate(a); err != nil {
			return err
		}
		if seen[a.ID] {
			return fmt.Errorf("agent %s defined twice", a.ID)
		}
		seen[a.ID] = true
	}
	return nil
}

// CheckReads refuses an agent that reads a kind of context outside known, the
// kinds user context holds, so a typo in an agent file fails when it is
// published instead of failing every session start with that agent.
func CheckReads(defs []Agent, known []string) error {
	for _, a := range defs {
		for _, kind := range a.Reads {
			if !slices.Contains(known, kind) {
				return fmt.Errorf("agent %s reads %q, a kind of context user context doesn't hold (%s)", a.Name, kind, strings.Join(known, ", "))
			}
		}
	}
	return nil
}

var modelName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// IsModelName reports whether name can name a model: the form a model
// service's names take, checked at publish so a stray character fails there
// and not on every turn.
func IsModelName(name string) bool { return modelName.MatchString(name) }

var blockName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// IsBlockName reports whether name can name a block of text a product sends,
// as session context or as screen input: an agent's instructions refer to it,
// and the prompt renders it as a tag, so nothing else may appear in one.
func IsBlockName(name string) bool { return blockName.MatchString(name) }

func validate(a Agent) error {
	for i, name := range a.SessionContext {
		if !IsBlockName(name) {
			return fmt.Errorf("agent %s: session context %q must be lower-case letters, digits and _, at most 32", a.ID, name)
		}
		if slices.Contains(a.SessionContext[:i], name) {
			return fmt.Errorf("agent %s: session context %q listed twice", a.ID, name)
		}
	}
	if a.Model != "" && !IsModelName(a.Model) {
		return fmt.Errorf("agent %s: model %q must be lower-case letters, digits, dots and dashes, starting with a letter or digit, at most 64", a.ID, a.Model)
	}
	switch {
	case a.ID == uuid.Nil():
		return errors.New("agent without an ID")
	case a.ID[6]>>4 != 7: // the version nibble: IDs are time-ordered UUIDv7, like every other ID
		return fmt.Errorf("agent ID %s is not a UUIDv7", a.ID)
	case a.Name == "":
		return fmt.Errorf("agent %s has no name", a.ID)
	case a.Persona == "":
		return fmt.Errorf("agent %s has no persona", a.ID)
	}
	return nil
}

// copy returns the agent with its own Reads, so no caller can change which
// context kinds the catalog gives later sessions.
func (a Agent) copy() Agent {
	a.Reads = slices.Clone(a.Reads)
	a.SessionContext = slices.Clone(a.SessionContext)
	return a
}
