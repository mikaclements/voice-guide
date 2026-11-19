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
	"maps"
	"slices"
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
	// What to note about a session once it ends, and what to grade it on.
	Summary summarySettings `yaml:"summary"`
	// Who speaks first: agent or user, user unless set.
	Opens string `yaml:"opens"`
	// The model that answers this agent's turns, the server's unless set.
	Model string `yaml:"model"`
	// The voice that speaks this agent's replies, and how fast, 1 the voice's own pace; the
	// server's unless set, and a session's start may name others. A pointer, so a speed of 0
	// written out is refused instead of read as unset.
	Voice string   `yaml:"voice"`
	Speed *float64 `yaml:"speed"`
}

// summarySettings is the summary section: a plain string is the instruction
// alone, as agent files written before grading say it; a mapping adds the
// model that writes the summary and what the session is graded on.
type summarySettings struct {
	Instruction string
	Model       string
	Scale       []int
	Levels      map[int]string
	Criteria    []criterionSettings
}

type summaryMapping struct {
	Instruction string              `yaml:"instruction"`
	Model       string              `yaml:"model"`
	Scale       []int               `yaml:"scale"`
	Levels      map[int]string      `yaml:"levels"`
	Criteria    []criterionSettings `yaml:"criteria"`
}

type criterionSettings struct {
	Name     string `yaml:"name"`
	LooksFor string `yaml:"looks_for"`
}

// UnmarshalYAML reads the section as a string, else as a mapping. It takes
// the decoding function rather than the node, since that decodes with the
// file's own decoder, which refuses an unknown key inside the mapping too.
func (s *summarySettings) UnmarshalYAML(decode func(any) error) error {
	if err := decode(&s.Instruction); err == nil {
		return nil
	}
	var m summaryMapping
	if err := decode(&m); err != nil {
		return err
	}
	if m.Instruction == "" {
		return errors.New("summary: a summary mapping needs an instruction")
	}
	*s = summarySettings(m)
	return nil
}

// grading turns the section's scale, levels and criteria into the agent's
// grading, checking what only the file's form shows: the scale's two ends,
// and a scale and criteria given together. Publishing checks the rest.
func (s summarySettings) grading() (agents.Grading, error) {
	if (len(s.Scale) > 0 || len(s.Levels) > 0) != (len(s.Criteria) > 0) {
		return agents.Grading{}, errors.New("summary: scale and criteria come together, or neither")
	}
	if len(s.Criteria) == 0 {
		return agents.Grading{}, nil
	}
	if len(s.Scale) != 2 {
		return agents.Grading{}, fmt.Errorf("summary: scale %v: expected [lowest, highest]", s.Scale)
	}
	g := agents.Grading{Lowest: s.Scale[0], Highest: s.Scale[1]}
	// A map has no order; the levels render in score order, so every publish gives the same bytes.
	for _, score := range slices.Sorted(maps.Keys(s.Levels)) {
		g.Levels = append(g.Levels, agents.Level{Score: score, Means: strings.TrimSpace(s.Levels[score])})
	}
	for _, c := range s.Criteria {
		g.Criteria = append(g.Criteria, agents.Criterion{Name: c.Name, LooksFor: strings.TrimSpace(c.LooksFor)})
	}
	return g, nil
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
	if s.Voice != "" && !agents.IsVoiceName(s.Voice) {
		return agents.Agent{}, fmt.Errorf("%s: voice %q: expected a language and a style, such as af_heart: two lower-case letters, an underscore, then lower-case letters and digits", name, s.Voice)
	}
	if s.Summary.Model != "" && !agents.IsModelName(s.Summary.Model) {
		return agents.Agent{}, fmt.Errorf("%s: summary model %q: expected lower-case letters, digits, dots and dashes, starting with a letter or digit, at most 64", name, s.Summary.Model)
	}
	grading, err := s.Summary.grading()
	if err != nil {
		return agents.Agent{}, fmt.Errorf("%s: %w", name, err)
	}
	var speed float64
	if s.Speed != nil {
		if speed = *s.Speed; !agents.IsSpeed(speed) {
			return agents.Agent{}, fmt.Errorf("%s: speed %v: expected from %v to %v", name, speed, agents.MinSpeed, agents.MaxSpeed)
		}
	}
	return agents.Agent{
		ID:             id,
		Name:           s.Name,
		Description:    s.Description,
		Reads:          s.Reads,
		SessionContext: s.SessionContext,
		Persona:        string(persona),
		// A block's last line break is the YAML's, not the instruction's.
		Summary:      strings.TrimSpace(s.Summary.Instruction),
		SummaryModel: s.Summary.Model,
		Grading:      grading,
		Opens:        s.Opens == "agent",
		Model:        s.Model,
		Voice:        s.Voice,
		Speed:        speed,
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
