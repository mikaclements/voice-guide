package agents_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/agents/store/fake"
)

// scoredKeeper is the lighthouse keeper with a summary written on its own model, with a schema.
func scoredKeeper() agents.Agent {
	a := lighthouseKeeper()
	a.Summary = "Note the ships you guided."
	a.SummaryModel = "claude-opus-5-5"
	a.SummarySchema = json.RawMessage(`{"properties":{"fog_calls":{"type":"integer"}},"type":"object"}`)
	return a
}

func TestPublishSavesTheSummaryModelAndSchemaWithTheVersion(t *testing.T) {
	store := fake.New()
	publish(t, store, scoredKeeper())
	got, err := store.Version(context.Background(), workspace, keeperID, 1)
	if err != nil || got.SummaryModel != "claude-opus-5-5" || string(got.SummarySchema) != string(scoredKeeper().SummarySchema) {
		t.Errorf("Version = %+v, %v; want the summary model and schema kept", got, err)
	}
}

func TestPublishingTheSameSummarySettingsAgainSavesNoVersionAndAChangeSavesTheNext(t *testing.T) {
	store := fake.New()
	publish(t, store, scoredKeeper())
	if again := publish(t, store, scoredKeeper()); again[0].New {
		t.Errorf("Publish of the same summary settings = %+v, want no new version", again)
	}
	for name, change := range map[string]func(*agents.Agent){
		"the summary model": func(a *agents.Agent) { a.SummaryModel = "claude-haiku-5-5" },
		"the schema":        func(a *agents.Agent) { a.SummarySchema = json.RawMessage(`{"type":"object"}`) },
		"no schema":         func(a *agents.Agent) { a.SummarySchema = nil },
	} {
		a := scoredKeeper()
		change(&a)
		if got := publish(t, store, a); !got[0].New {
			t.Errorf("Publish with a changed %s = %+v, want a new version", name, got)
		}
		publish(t, store, scoredKeeper())
	}
}

func TestPublishRefusesASummaryModelOrSchemaOutOfForm(t *testing.T) {
	for name, set := range map[string]func(*agents.Agent){
		"a summary model's name":         func(a *agents.Agent) { a.SummaryModel = "Claude Opus" },
		"a summary model and no summary": func(a *agents.Agent) { a.Summary, a.SummarySchema = "", nil },
		"a schema and no summary":        func(a *agents.Agent) { a.Summary, a.SummaryModel = "", "" },
		"a schema that isn't JSON":       func(a *agents.Agent) { a.SummarySchema = json.RawMessage(`{"type":`) },
		"a schema that is a list":        func(a *agents.Agent) { a.SummarySchema = json.RawMessage(`[{"type":"object"}]`) },
		"a schema of an array":           func(a *agents.Agent) { a.SummarySchema = json.RawMessage(`{"type":"array"}`) },
		"a schema with no type":          func(a *agents.Agent) { a.SummarySchema = json.RawMessage(`{"properties":{}}`) },
		"a schema of nothing":            func(a *agents.Agent) { a.SummarySchema = json.RawMessage(`null`) },
		"a schema over the cap": func(a *agents.Agent) {
			a.SummarySchema = json.RawMessage(`{"type":"object","description":"` + strings.Repeat("a", agents.MaxSummarySchemaChars) + `"}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := scoredKeeper()
			set(&a)
			if _, err := agents.Publish(context.Background(), fake.New(), workspace, []agents.Agent{a}); err == nil {
				t.Error("Publish accepted it")
			}
		})
	}
}

func TestPublishAcceptsASchemaAtTheCap(t *testing.T) {
	a := scoredKeeper()
	head, tail := `{"type":"object","description":"`, `"}`
	a.SummarySchema = json.RawMessage(head + strings.Repeat("a", agents.MaxSummarySchemaChars-len(head)-len(tail)) + tail)
	if _, err := agents.Publish(context.Background(), fake.New(), workspace, []agents.Agent{a}); err != nil {
		t.Errorf("Publish = %v, want a schema of exactly %d characters accepted", err, agents.MaxSummarySchemaChars)
	}
}

func TestNoCallerCanChangeAnAgentsSummarySchema(t *testing.T) {
	store := fake.New()
	publish(t, store, scoredKeeper())
	catalog := agents.NewCatalog(store)
	first, err := catalog.Version(context.Background(), workspace, keeperID, 1)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	first.SummarySchema[2] = 'X'
	again, err := catalog.Version(context.Background(), workspace, keeperID, 1)
	if err != nil || string(again.SummarySchema) != string(scoredKeeper().SummarySchema) {
		t.Errorf("Version after a caller's change = %s, %v; want the schema as published", again.SummarySchema, err)
	}
}
