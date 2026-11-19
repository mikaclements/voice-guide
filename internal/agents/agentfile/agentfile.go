// Package agentfile reads agent files: Markdown, one per agent
// (lighthouse-keeper.md). Each file opens with a YAML settings block between
// --- lines; the rest is the persona, sent to the model as written. Publishing
// reads them to save each agent's next version.
package agentfile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"uuid"

	"go.yaml.in/yaml/v3"

	"github.com/mikaclements/voice-guide/internal/agents"
)

type settings struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Reads       []string `yaml:"reads"`
	// What the product must send when a session starts, by name.
	SessionContext []string `yaml:"session_context"`
	// What to note about a session once it ends.
	Summary string `yaml:"summary"`
	// Who speaks first: agent or user, user unless set.
	Opens string `yaml:"opens"`
	// The model that answers this agent's turns, the server's unless set.
	Model string `yaml:"model"`
}

// Load reads every agent in fsys. It refuses anything it doesn't recognise,
// so a misplaced file fails the publish instead of being silently skipped.
// Hidden files (a Finder's .DS_Store, an editor's swap file) are skipped, the
// way go:embed skips them. An fs.FS read can't be interrupted, so ctx is
// checked between files.
func Load(ctx context.Context, fsys fs.FS) ([]agents.Agent, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read agents folder: %w", err)
	}
	var all []agents.Agent
	fileOf := make(map[uuid.UUID]string)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("load agents: %w", err)
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			return nil, fmt.Errorf("%s: expected only agent files named <name>.md", e.Name())
		}
		a, err := loadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		if other, seen := fileOf[a.ID]; seen {
			return nil, fmt.Errorf("agent ID %s is used by both %s and %s", a.ID, other, e.Name())
		}
		fileOf[a.ID] = e.Name()
		all = append(all, a)
	}
	if len(all) == 0 {
		// A wrong but existing folder would otherwise publish nothing and seem to succeed.
		return nil, errors.New("agents folder has no agent files")
	}
	return all, nil
}

func loadFile(fsys fs.FS, name string) (agents.Agent, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return agents.Agent{}, fmt.Errorf("read %s: %w", name, err)
	}
	head, persona, err := split(data)
	if err != nil {
		return agents.Agent{}, fmt.Errorf("%s: %w", name, err)
	}
	var s settings
	dec := yaml.NewDecoder(bytes.NewReader(head))
	// An unknown key is refused rather than ignored: a typo would otherwise
	// drop a setting without anyone noticing.
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return agents.Agent{}, fmt.Errorf("%s: settings: %w", name, err)
	}
	id, err := uuid.Parse(s.ID)
	if err != nil {
		return agents.Agent{}, fmt.Errorf("%s: id %q: %w", name, s.ID, err)
	}
	if s.Opens != "" && s.Opens != "agent" && s.Opens != "user" {
		return agents.Agent{}, fmt.Errorf("%s: opens %q: expected agent or user", name, s.Opens)
	}
	if s.Model != "" && !agents.IsModelName(s.Model) {
		return agents.Agent{}, fmt.Errorf("%s: model %q: expected lower-case letters, digits, dots and dashes, starting with a letter or digit, at most 64", name, s.Model)
	}
	return agents.Agent{
		ID:             id,
		Name:           s.Name,
		Description:    s.Description,
		Reads:          s.Reads,
		SessionContext: s.SessionContext,
		Persona:        string(persona),
		// A block's last line break is the YAML's, not the instruction's.
		Summary: strings.TrimSpace(s.Summary),
		Opens:   s.Opens == "agent",
		Model:   s.Model,
	}, nil
}

// split separates the settings block from the persona.
func split(data []byte) (head, persona []byte, err error) {
	const fence = "---\n"
	rest, ok := bytes.CutPrefix(data, []byte(fence))
	if !ok {
		return nil, nil, errors.New("expected a settings block opening with ---")
	}
	head, persona, ok = bytes.Cut(rest, []byte("\n"+fence))
	if !ok {
		return nil, nil, errors.New("settings block is not closed with ---")
	}
	return append(head, '\n'), persona, nil
}
