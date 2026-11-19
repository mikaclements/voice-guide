package agents

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
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
	// SummaryModel names the model that writes the agent's summaries; empty,
	// the model that answers its turns writes them.
	SummaryModel string
	// Grading is what the agent's sessions are graded on once they end; the
	// zero value grades nothing.
	Grading Grading
	// Opens says the agent speaks first in its sessions, rather than waiting
	// for the user.
	Opens bool
	// Model names the model that answers this agent's turns and writes its
	// summaries; empty, the server's own model answers.
	Model string
	// Voice names the voice that speaks this agent's replies, as the
	// text-to-speech service names its voices; empty, the server's voice
	// speaks. A session's start may name another, which then wins.
	Voice string
	// Speed is how fast the voice speaks, 1 the voice's own pace; 0 is not
	// set, and the server's speed applies. A session's start may name
	// another, which then wins.
	Speed float64
}

// Grading is how an agent's sessions are graded once they end: each
// criterion scored on the scale from Lowest to Highest. Its author's text,
// published with the agent like the persona.
type Grading struct {
	Lowest, Highest int
	// Levels say what some of the scores mean, in order of score; there may
	// be none.
	Levels   []Level
	Criteria []Criterion
}

// Level says what one score on a grading's scale means.
type Level struct {
	Score int
	Means string
}

// Criterion is one thing a session is graded on.
type Criterion struct {
	Name     string
	LooksFor string
}

// IsSet reports whether the grading grades anything: a scale comes only with
// criteria.
func (g Grading) IsSet() bool { return len(g.Criteria) > 0 }

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

// CheckVoices refuses an agent that names a voice outside offered, the voices the text-to-speech
// service has, so a voice it lacks fails when the agent is published instead of failing every
// phrase its sessions speak.
func CheckVoices(defs []Agent, offered []string) error {
	for _, a := range defs {
		if a.Voice != "" && !slices.Contains(offered, a.Voice) {
			return fmt.Errorf("agent %s names the voice %q, which the text-to-speech service doesn't offer", a.Name, a.Voice)
		}
	}
	return nil
}

// Limits on a grading, round guesses at what an author needs: a scorecard of a
// dozen criteria is long already, and each text is a sentence or a short
// paragraph. To be revisited when an agent's grading presses against one.
const (
	MaxCriteria      = 20
	MaxLooksForChars = 1000
	MaxLevelChars    = 500
	// A scale wider than 0 to 100 adds nothing a grader can tell apart.
	MaxScaleSpan = 100
)

var modelName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// IsModelName reports whether name can name a model: the form a model
// service's names take, checked at publish so a stray character fails there
// and not on every turn.
func IsModelName(name string) bool { return modelName.MatchString(name) }

// A style is at most 32 characters: Kokoro's styles are about ten (gongitsune is the longest
// seen), so 32 leaves room for new voices while refusing a name that is really text. A round
// guess, to be checked when a voice with a longer style appears.
var voiceName = regexp.MustCompile(`^[a-z]{2}_[a-z0-9]{1,32}$`)

// IsVoiceName reports whether name can name a voice: a language and a style
// joined by an underscore, as Kokoro names them (af_heart), checked before a
// name reaches any request to the text-to-speech service.
func IsVoiceName(name string) bool { return voiceName.MatchString(name) }

// The speeds a voice may be asked to speak at: half the voice's pace to double
// it. A round guess at the range that stays understandable; to be checked by
// ear on Kokoro's voices once an agent asks for one near an end.
const (
	MinSpeed = 0.5
	MaxSpeed = 2.0
)

// IsSpeed reports whether speed lies within MinSpeed and MaxSpeed, the bounds
// included. NaN is not a speed.
func IsSpeed(speed float64) bool { return speed >= MinSpeed && speed <= MaxSpeed }

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
	if a.Voice != "" && !IsVoiceName(a.Voice) {
		return fmt.Errorf("agent %s: voice %q must be a language and a style, such as af_heart: two lower-case letters, an underscore, then lower-case letters and digits", a.ID, a.Voice)
	}
	if a.Speed != 0 && !IsSpeed(a.Speed) {
		return fmt.Errorf("agent %s: speed %v must be from %v to %v", a.ID, a.Speed, MinSpeed, MaxSpeed)
	}
	if a.SummaryModel != "" && !IsModelName(a.SummaryModel) {
		return fmt.Errorf("agent %s: summary model %q must be lower-case letters, digits, dots and dashes, starting with a letter or digit, at most 64", a.ID, a.SummaryModel)
	}
	if a.Summary == "" && (a.SummaryModel != "" || a.Grading.IsSet()) {
		return fmt.Errorf("agent %s: a summary model or grading needs a summary instruction", a.ID)
	}
	if err := validateGrading(a.Grading); err != nil {
		return fmt.Errorf("agent %s: %w", a.ID, err)
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

func validateGrading(g Grading) error {
	if !g.IsSet() {
		if g.Lowest != 0 || g.Highest != 0 || len(g.Levels) > 0 {
			return errors.New("a grading scale needs criteria")
		}
		return nil
	}
	if g.Lowest >= g.Highest || g.Highest-g.Lowest > MaxScaleSpan {
		return fmt.Errorf("grading scale %d to %d must have its lowest below its highest, at most %d apart", g.Lowest, g.Highest, MaxScaleSpan)
	}
	for i, l := range g.Levels {
		switch {
		case l.Score < g.Lowest || l.Score > g.Highest:
			return fmt.Errorf("grading level %d is outside the scale %d to %d", l.Score, g.Lowest, g.Highest)
		case i > 0 && l.Score <= g.Levels[i-1].Score:
			return fmt.Errorf("grading level %d must come after level %d, once", l.Score, g.Levels[i-1].Score)
		case strings.TrimSpace(l.Means) == "" || utf8.RuneCountInString(l.Means) > MaxLevelChars:
			return fmt.Errorf("grading level %d must say what it means in at most %d characters", l.Score, MaxLevelChars)
		}
	}
	if len(g.Criteria) > MaxCriteria {
		return fmt.Errorf("grading has %d criteria, at most %d", len(g.Criteria), MaxCriteria)
	}
	for i, c := range g.Criteria {
		switch {
		case !IsBlockName(c.Name):
			return fmt.Errorf("grading criterion %q must be lower-case letters, digits and _, at most 32", c.Name)
		case slices.ContainsFunc(g.Criteria[:i], func(o Criterion) bool { return o.Name == c.Name }):
			return fmt.Errorf("grading criterion %q listed twice", c.Name)
		case strings.TrimSpace(c.LooksFor) == "" || utf8.RuneCountInString(c.LooksFor) > MaxLooksForChars:
			return fmt.Errorf("grading criterion %q must say what it looks for in at most %d characters", c.Name, MaxLooksForChars)
		}
	}
	return nil
}

// copy returns the agent with its own slices, so no caller can change which
// context kinds or criteria the catalog gives later sessions.
func (a Agent) copy() Agent {
	a.Reads = slices.Clone(a.Reads)
	a.SessionContext = slices.Clone(a.SessionContext)
	a.Grading.Levels = slices.Clone(a.Grading.Levels)
	a.Grading.Criteria = slices.Clone(a.Grading.Criteria)
	return a
}
