package agents

import (
	"context"
	"encoding/json"
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
	// SummaryModel names the model that writes the agent's summaries; empty,
	// the model that answers its turns writes them.
	SummaryModel string
	// SummarySchema is a JSON Schema of the agent author's own, an object,
	// for a structured part of the summary; nil, the summary is text alone.
	SummarySchema json.RawMessage
	// SummaryDocuments names the documents the summary's call reads. One
	// also under SessionContext reaches the agent too; one only here is a
	// document a start may send that the conversation's prompt never holds:
	// what the agent must not see while it talks, but the summary draws on.
	SummaryDocuments []string
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

// Newest returns the newest version of one of the workspace's agents, or
// ErrNotFound when the workspace has no such agent. It reads the workspace's
// newest versions and picks the one: a workspace holds a few agents, and this
// is read only by whoever publishes them.
func (c *Catalog) Newest(ctx context.Context, workspace, agent uuid.UUID) (Version, error) {
	list, err := c.Available(ctx, workspace)
	if err != nil {
		return Version{}, err
	}
	for _, v := range list {
		if v.ID == agent {
			return v, nil
		}
	}
	return Version{}, ErrNotFound
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
			return unknownVoice{fmt.Sprintf("agent %s names the voice %q, which the text-to-speech service doesn't offer", a.Name, a.Voice)}
		}
	}
	return nil
}

// MaxSummarySchemaChars bounds a summary's schema, encoded as JSON: a round
// guess, room for a few dozen fields each with a description, while a schema
// stays small beside the transcript it is asked with. To be revisited when an
// agent's schema presses against it.
const MaxSummarySchemaChars = 16000

// CheckSummarySchema refuses a summary schema that isn't a JSON object whose
// type is object, or is over MaxSummarySchemaChars: the summary's structured
// part is one object, so a schema for anything else could never be met.
func CheckSummarySchema(schema json.RawMessage) error {
	if len(schema) > MaxSummarySchemaChars {
		return fmt.Errorf("summary schema is %d characters as JSON, at most %d", len(schema), MaxSummarySchemaChars)
	}
	var top map[string]any
	if err := json.Unmarshal(schema, &top); err != nil {
		return fmt.Errorf("summary schema must be a JSON object: %w", err)
	}
	if top["type"] != "object" {
		return errors.New("summary schema must have type: object at its top level")
	}
	return checkSubschema("the schema", top)
}

// checkSubschema refuses a part of a summary schema that says nothing of what
// may stand there: no type, alternatives, value or reference. The Claude API
// holds an answer to a schema only when every part does, so a part with none
// of them would make every summary of the agent's fail there.
func checkSubschema(where string, s map[string]any) error {
	if !slices.ContainsFunc([]string{"type", "anyOf", "oneOf", "allOf", "enum", "const", "$ref"}, func(k string) bool { _, ok := s[k]; return ok }) {
		return fmt.Errorf("summary schema: %s needs a type, anyOf, oneOf, allOf, enum, const or $ref", where)
	}
	for _, key := range []string{"properties", "$defs"} {
		named, _ := s[key].(map[string]any)
		for name, sub := range named {
			if err := checkPart(where+" "+key+"."+name, sub); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		listed, _ := s[key].([]any)
		for i, sub := range listed {
			if err := checkPart(fmt.Sprintf("%s %s[%d]", where, key, i), sub); err != nil {
				return err
			}
		}
	}
	if items, ok := s["items"]; ok {
		return checkPart(where+" items", items)
	}
	return nil
}

// checkPart checks one part of a summary schema, which must be an object.
func checkPart(where string, part any) error {
	s, ok := part.(map[string]any)
	if !ok {
		return fmt.Errorf("summary schema: %s must be a schema object", where)
	}
	return checkSubschema(where, s)
}

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

// summaryRecordTags are the tags the summary's record renders its own parts in.
var summaryRecordTags = []string{"instructions", "transcript", "documents", "screen"}

var blockName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// IsBlockName reports whether name can name a block of text a product sends,
// as session context or as screen input: an agent's instructions refer to it,
// and the prompt renders it as a tag, so nothing else may appear in one.
func IsBlockName(name string) bool { return blockName.MatchString(name) }

// hasNUL reports a NUL in any text of the agent, raw in the file or written as an escape in its YAML
// or its schema's JSON: PostgreSQL stores none in text or jsonb, so it must be refused here as a
// problem with the file, not met at the save as a failure to retry.
func hasNUL(a Agent) bool {
	texts := slices.Concat([]string{a.Name, a.Description, a.Persona, a.Summary, a.SummaryModel, a.Model, a.Voice},
		a.Reads, a.SessionContext, a.SummaryDocuments)
	return slices.ContainsFunc(texts, func(s string) bool { return strings.ContainsRune(s, 0) }) ||
		strings.Contains(string(a.SummarySchema), `\u0000`)
}

func validate(a Agent) error {
	if hasNUL(a) {
		return fmt.Errorf("agent %s: a NUL character can't be stored; remove it", a.ID)
	}
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
	if a.Summary == "" && (a.SummaryModel != "" || a.SummarySchema != nil || len(a.SummaryDocuments) > 0) {
		return fmt.Errorf("agent %s: a summary model, schema or documents need a summary instruction", a.ID)
	}
	for i, name := range a.SummaryDocuments {
		switch {
		case !IsBlockName(name):
			return fmt.Errorf("agent %s: summary document %q must be lower-case letters, digits and _, at most 32", a.ID, name)
		case slices.Contains(a.SummaryDocuments[:i], name):
			return fmt.Errorf("agent %s: summary document %q listed twice", a.ID, name)
		case slices.Contains(summaryRecordTags, name):
			// The summary's record renders each document as a tag beside these, so one of these
			// names would read as the wrong part.
			return fmt.Errorf("agent %s: summary document %q is a name the summary's record uses for its own parts", a.ID, name)
		}
	}
	if a.SummarySchema != nil {
		if err := CheckSummarySchema(a.SummarySchema); err != nil {
			return fmt.Errorf("agent %s: %w", a.ID, err)
		}
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

// copy returns the agent with its own slices, so no caller can change which
// context kinds or summary schema the catalog gives later sessions.
func (a Agent) copy() Agent {
	a.Reads = slices.Clone(a.Reads)
	a.SessionContext = slices.Clone(a.SessionContext)
	a.SummarySchema = slices.Clone(a.SummarySchema)
	a.SummaryDocuments = slices.Clone(a.SummaryDocuments)
	return a
}
