package agents_test

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/agents/store/fake"
)

// gradedKeeper is the lighthouse keeper with a summary written on its own model and graded on two criteria.
func gradedKeeper() agents.Agent {
	a := lighthouseKeeper()
	a.Summary = "Note the ships you guided."
	a.SummaryModel = "claude-opus-5-5"
	a.Grading = agents.Grading{
		Lowest: 1, Highest: 5,
		Levels:   []agents.Level{{Score: 1, Means: "Lost a ship."}, {Score: 5, Means: "Guided every ship home."}},
		Criteria: []agents.Criterion{{Name: "fog_calls", LooksFor: "Answers every call in the fog."}, {Name: "lamp_care", LooksFor: "Keeps the lamp lit."}},
	}
	return a
}

func TestPublishSavesTheSummaryModelAndGradingWithTheVersion(t *testing.T) {
	store := fake.New()
	publish(t, store, gradedKeeper())
	got, err := store.Version(context.Background(), workspace, keeperID, 1)
	if err != nil || got.SummaryModel != "claude-opus-5-5" || len(got.Grading.Criteria) != 2 || got.Grading.Levels[1].Means != "Guided every ship home." {
		t.Errorf("Version = %+v, %v; want the summary model and grading kept", got, err)
	}
}

func TestPublishingTheSameGradingAgainSavesNoVersionAndAChangedOneSavesTheNext(t *testing.T) {
	store := fake.New()
	publish(t, store, gradedKeeper())
	if again := publish(t, store, gradedKeeper()); again[0].New {
		t.Errorf("Publish of the same grading = %+v, want no new version", again)
	}
	for name, change := range map[string]func(*agents.Agent){
		"the summary model": func(a *agents.Agent) { a.SummaryModel = "claude-haiku-5-5" },
		"a criterion":       func(a *agents.Agent) { a.Grading.Criteria[1].LooksFor = "Keeps the lamp lit all night." },
		"a level":           func(a *agents.Agent) { a.Grading.Levels[0].Means = "Lost two ships." },
		"the scale":         func(a *agents.Agent) { a.Grading.Highest = 10 },
	} {
		a := gradedKeeper()
		change(&a)
		if got := publish(t, store, a); !got[0].New {
			t.Errorf("Publish with a changed %s = %+v, want a new version", name, got)
		}
		publish(t, store, gradedKeeper())
	}
}

func TestPublishRefusesAGradingOutOfForm(t *testing.T) {
	many := make([]agents.Criterion, agents.MaxCriteria+1)
	for i := range many {
		many[i] = agents.Criterion{Name: fmt.Sprintf("c%d", i), LooksFor: "Something."}
	}
	for name, set := range map[string]func(*agents.Agent){
		"a summary model's name":         func(a *agents.Agent) { a.SummaryModel = "Claude Opus" },
		"a summary model and no summary": func(a *agents.Agent) { a.Summary, a.Grading = "", agents.Grading{} },
		"grading and no summary":         func(a *agents.Agent) { a.Summary, a.SummaryModel = "", "" },
		"a scale without criteria":       func(a *agents.Agent) { a.Grading.Criteria = nil; a.Grading.Levels = nil },
		"levels without criteria":        func(a *agents.Agent) { a.Grading = agents.Grading{Levels: a.Grading.Levels} },
		"criteria without a scale":       func(a *agents.Agent) { a.Grading.Lowest, a.Grading.Highest, a.Grading.Levels = 0, 0, nil },
		"the lowest above the highest":   func(a *agents.Agent) { a.Grading.Lowest, a.Grading.Highest = 5, 1 },
		"a scale too wide": func(a *agents.Agent) {
			a.Grading.Lowest, a.Grading.Highest, a.Grading.Levels = 0, agents.MaxScaleSpan+1, nil
		},
		"a level below the scale":         func(a *agents.Agent) { a.Grading.Levels[0].Score = 0 },
		"a level above the scale":         func(a *agents.Agent) { a.Grading.Levels[1].Score = 6 },
		"a level twice":                   func(a *agents.Agent) { a.Grading.Levels[1].Score = 1 },
		"a level meaning nothing":         func(a *agents.Agent) { a.Grading.Levels[0].Means = " " },
		"a level too long":                func(a *agents.Agent) { a.Grading.Levels[0].Means = strings.Repeat("a", agents.MaxLevelChars+1) },
		"a criterion's name":              func(a *agents.Agent) { a.Grading.Criteria[0].Name = "Fog calls" },
		"a criterion twice":               func(a *agents.Agent) { a.Grading.Criteria[1].Name = "fog_calls" },
		"a criterion looking for nothing": func(a *agents.Agent) { a.Grading.Criteria[0].LooksFor = "" },
		"a criterion too long":            func(a *agents.Agent) { a.Grading.Criteria[0].LooksFor = strings.Repeat("a", agents.MaxLooksForChars+1) },
		"too many criteria":               func(a *agents.Agent) { a.Grading.Criteria = many },
		"the widest scale overflowing": func(a *agents.Agent) {
			a.Grading.Lowest, a.Grading.Highest, a.Grading.Levels = math.MinInt, math.MaxInt, nil
		},
		"a lowest end too low": func(a *agents.Agent) {
			a.Grading.Lowest, a.Grading.Highest, a.Grading.Levels = -agents.MaxScaleEnd-1, -agents.MaxScaleEnd+5, nil
		},
		"a highest end too high": func(a *agents.Agent) {
			a.Grading.Lowest, a.Grading.Highest, a.Grading.Levels = agents.MaxScaleEnd-5, agents.MaxScaleEnd+1, nil
		},
		"a criterion named criteria": func(a *agents.Agent) { a.Grading.Criteria[0].Name = "criteria" },
		"a criterion named scale":    func(a *agents.Agent) { a.Grading.Criteria[0].Name = "scale" },
	} {
		t.Run(name, func(t *testing.T) {
			a := gradedKeeper()
			set(&a)
			if _, err := agents.Publish(context.Background(), fake.New(), workspace, []agents.Agent{a}); err == nil {
				t.Error("Publish accepted it")
			}
		})
	}
}

func TestPublishAcceptsAGradingAtItsLimits(t *testing.T) {
	a := gradedKeeper()
	a.Grading.Lowest, a.Grading.Highest = 0, agents.MaxScaleSpan
	a.Grading.Levels = []agents.Level{{Score: 0, Means: strings.Repeat("a", agents.MaxLevelChars)}, {Score: agents.MaxScaleSpan, Means: "All."}}
	a.Grading.Criteria = make([]agents.Criterion, agents.MaxCriteria)
	for i := range a.Grading.Criteria {
		a.Grading.Criteria[i] = agents.Criterion{Name: fmt.Sprintf("c%d", i), LooksFor: strings.Repeat("a", agents.MaxLooksForChars)}
	}
	if _, err := agents.Publish(context.Background(), fake.New(), workspace, []agents.Agent{a}); err != nil {
		t.Errorf("Publish = %v, want a grading at every limit accepted", err)
	}
}

func TestPublishAcceptsAScaleAtEitherEndsLimit(t *testing.T) {
	for _, ends := range [][2]int{{-agents.MaxScaleEnd, -agents.MaxScaleEnd + 4}, {agents.MaxScaleEnd - 4, agents.MaxScaleEnd}} {
		a := gradedKeeper()
		a.Grading.Lowest, a.Grading.Highest, a.Grading.Levels = ends[0], ends[1], nil
		if _, err := agents.Publish(context.Background(), fake.New(), workspace, []agents.Agent{a}); err != nil {
			t.Errorf("Publish with the scale %v = %v, want it accepted", ends, err)
		}
	}
}

func TestNoCallerCanChangeAnAgentsCriteria(t *testing.T) {
	store := fake.New()
	publish(t, store, gradedKeeper())
	catalog := agents.NewCatalog(store)
	first, err := catalog.Version(context.Background(), workspace, keeperID, 1)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	first.Grading.Criteria[0].Name = "changed"
	first.Grading.Levels[0].Means = "changed"
	again, err := catalog.Version(context.Background(), workspace, keeperID, 1)
	if err != nil || again.Grading.Criteria[0].Name != "fog_calls" || again.Grading.Levels[0].Means != "Lost a ship." {
		t.Errorf("Version after a caller's change = %+v, %v; want the grading as published", again.Grading, err)
	}
}
