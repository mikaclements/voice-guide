package agents_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/agents/store/fake"
)

func lighthouseKeeper() agents.Agent {
	return agents.Agent{
		ID: keeperID, Name: keeperName, Description: "Talks ships through the fog.",
		Reads: []string{"profile"}, Persona: "You answer every call from the sea.",
	}
}

func ferryCaptain() agents.Agent {
	return agents.Agent{ID: captainID, Name: "Ferry captain", Persona: "You know every crossing by heart."}
}

func publish(t *testing.T, store agents.Versions, defs ...agents.Agent) []agents.Published {
	t.Helper()
	got, err := agents.Publish(context.Background(), store, workspace, defs)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return got
}

func TestPublishSavesEachAgentAsItsFirstVersion(t *testing.T) {
	store := fake.New()
	got := publish(t, store, lighthouseKeeper(), ferryCaptain())

	want := []agents.Published{
		{Agent: keeperID, Name: keeperName, Number: 1, New: true},
		{Agent: captainID, Name: "Ferry captain", Number: 1, New: true},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Publish = %+v, want %+v", got, want)
	}
	v, err := store.Version(context.Background(), workspace, keeperID, 1)
	if err != nil || v.Persona != lighthouseKeeper().Persona || !slices.Equal(v.Reads, []string{"profile"}) {
		t.Errorf("saved version = %+v, %v", v, err)
	}
}

func TestPublishingTheSameTextAgainSavesNoVersion(t *testing.T) {
	store := fake.New()
	publish(t, store, lighthouseKeeper())

	got := publish(t, store, lighthouseKeeper())
	if got[0].Number != 1 || got[0].New {
		t.Errorf("republishing = %+v, want version 1, not new", got[0])
	}
}

func TestAChangeToTheSettingsOrPersonaSavesTheNextVersion(t *testing.T) {
	changes := map[string]func(*agents.Agent){
		"persona":         func(a *agents.Agent) { a.Persona += " Keep the lamp lit." },
		"name":            func(a *agents.Agent) { a.Name = "Harbour keeper" },
		"description":     func(a *agents.Agent) { a.Description = "Guides boats home." },
		"context kinds":   func(a *agents.Agent) { a.Reads = append(a.Reads, "session_summary") },
		"session context": func(a *agents.Agent) { a.SessionContext = []string{"resume"} },
		"summary":         func(a *agents.Agent) { a.Summary = "Note the ships you guided." },
		"who opens":       func(a *agents.Agent) { a.Opens = true },
		"model":           func(a *agents.Agent) { a.Model = "claude-sonnet-5-5" },
		"voice":           func(a *agents.Agent) { a.Voice = "bf_emma" },
		"speed":           func(a *agents.Agent) { a.Speed = 1.25 },
		"summary documents": func(a *agents.Agent) {
			a.Summary, a.SummaryDocuments = "Note the ships you guided.", []string{"ship_log"}
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			store := fake.New()
			publish(t, store, lighthouseKeeper())
			changed := lighthouseKeeper()
			change(&changed)

			got := publish(t, store, changed)
			if got[0].Number != 2 || !got[0].New {
				t.Errorf("publishing a changed %s = %+v, want new version 2", name, got[0])
			}
		})
	}
}

func TestPublishChecksEveryAgentBeforeSavingAny(t *testing.T) {
	store := fake.New()
	noPersona := ferryCaptain()
	noPersona.Persona = ""

	if _, err := agents.Publish(context.Background(), store, workspace, []agents.Agent{lighthouseKeeper(), noPersona}); err == nil {
		t.Fatal("Publish accepted an agent without a persona")
	}
	if got, _ := store.Newest(context.Background(), workspace); len(got) != 0 {
		t.Errorf("saved %d versions after refusing the batch, want none", len(got))
	}
}

func TestPublishRefusesInvalidDefinitions(t *testing.T) {
	tests := []struct {
		name string
		defs []agents.Agent
	}{
		{"same ID twice", []agents.Agent{agent(keeperID, keeperName), agent(keeperID, "Ferry captain")}},
		{"missing ID", []agents.Agent{agent(uuid.Nil(), keeperName)}},
		{"ID that isn't a UUIDv7", []agents.Agent{agent(invalidV4, keeperName)}},
		{"missing name", []agents.Agent{agent(keeperID, "")}},
		{"missing persona", []agents.Agent{{ID: keeperID, Name: keeperName}}},
		{"session context in capitals", []agents.Agent{withSessionContext("Resume")}},
		{"session context starting with a digit", []agents.Agent{withSessionContext("2nd_resume")}},
		{"session context over 32 characters", []agents.Agent{withSessionContext("a_name_far_longer_than_thirty_two_characters")}},
		{"session context listed twice", []agents.Agent{withSessionContext("resume", "page", "resume")}},
		{"a summary document in capitals", []agents.Agent{withSummaryDocuments("Ship_log")}},
		{"a summary document listed twice", []agents.Agent{withSummaryDocuments("ship_log", "ship_log")}},
		{"a summary document named instructions", []agents.Agent{withSummaryDocuments("instructions")}},
		{"a summary document named transcript", []agents.Agent{withSummaryDocuments("transcript")}},
		{"a summary document named documents", []agents.Agent{withSummaryDocuments("documents")}},
		{"a summary document named screen", []agents.Agent{withSummaryDocuments("screen")}},
		{"summary documents without a summary", []agents.Agent{func() agents.Agent { a := withSummaryDocuments("ship_log"); a.Summary = ""; return a }()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := agents.Publish(context.Background(), fake.New(), workspace, tt.defs); err == nil {
				t.Error("Publish accepted an invalid definition")
			}
		})
	}
}

func TestAnAgentIDAnotherWorkspaceUsesIsRefused(t *testing.T) {
	store := fake.New()
	publish(t, store, lighthouseKeeper())

	_, err := agents.Publish(context.Background(), store, elsewhere, []agents.Agent{lighthouseKeeper()})
	if !errors.Is(err, agents.ErrAgentInAnotherWorkspace) {
		t.Errorf("Publish = %v, want ErrAgentInAnotherWorkspace", err)
	}
}

func withSessionContext(names ...string) agents.Agent {
	a := lighthouseKeeper()
	a.SessionContext = names
	return a
}

// withSummaryDocuments is the keeper summarizing its sessions, expecting the page as session
// context and names as documents its summary reads.
func withSummaryDocuments(names ...string) agents.Agent {
	a := withSessionContext("page")
	a.Summary, a.SummaryDocuments = "Note the ships you guided.", names
	return a
}

func TestAnAgentsSummaryDocumentsAreSavedWithItsVersion(t *testing.T) {
	store := fake.New()
	publish(t, store, withSummaryDocuments("ship_log", "tide_notes"))

	got, err := agents.NewCatalog(store).Version(context.Background(), workspace, keeperID, 1)
	if err != nil || !slices.Equal(got.SummaryDocuments, []string{"ship_log", "tide_notes"}) || !slices.Equal(got.SessionContext, []string{"page"}) {
		t.Errorf("version 1 = %+v, %v; want its summary documents, in order, apart from its session context", got, err)
	}
}

func TestADocumentBothTheAgentAndTheSummaryReadIsAccepted(t *testing.T) {
	if _, err := agents.Publish(context.Background(), fake.New(), workspace, []agents.Agent{withSummaryDocuments("page")}); err != nil {
		t.Errorf("Publish = %v, want a name in both lists accepted", err)
	}
}

func TestAnAgentsSessionContextIsSavedWithItsVersion(t *testing.T) {
	store := fake.New()
	publish(t, store, withSessionContext("resume", "current_page"))

	got, err := agents.NewCatalog(store).Version(context.Background(), workspace, keeperID, 1)
	if err != nil || !slices.Equal(got.SessionContext, []string{"resume", "current_page"}) {
		t.Errorf("version 1 = %+v, %v; want its session context, in order", got, err)
	}
}
