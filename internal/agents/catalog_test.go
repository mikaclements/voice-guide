package agents_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/agents/store/fake"
)

var (
	workspace = uuid.MustParse("01a1de8a-0000-7000-8000-000000000001")
	elsewhere = uuid.MustParse("01a1de8a-0000-7000-8000-000000000002")
	keeperID  = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a1")
	captainID = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a2")
	invalidV4 = uuid.MustParse("6f1c2c3e-8b1a-4c5d-9e7f-0a1b2c3d4e5f")
)

const keeperName = "Lighthouse keeper"

func agent(id uuid.UUID, name string) agents.Agent {
	return agents.Agent{ID: id, Name: name, Reads: []string{"profile"}, Persona: "You answer every call from the sea."}
}

// countingStore is a fake store that counts the exact versions it is asked for.
type countingStore struct {
	*fake.Versions
	reads int
}

func (s *countingStore) Version(ctx context.Context, workspace, agent uuid.UUID, number int) (agents.Version, error) {
	s.reads++
	return s.Versions.Version(ctx, workspace, agent, number)
}

// catalogWith returns a catalog over a fake store holding the published agents.
func catalogWith(t *testing.T, defs ...agents.Agent) (*agents.Catalog, *countingStore) {
	t.Helper()
	store := &countingStore{Versions: fake.New()}
	if len(defs) > 0 {
		publish(t, store, defs...)
	}
	return agents.NewCatalog(store), store
}

func available(t *testing.T, c *agents.Catalog, ws uuid.UUID) []agents.Version {
	t.Helper()
	got, err := c.Available(context.Background(), ws)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	return got
}

func TestAvailableListsTheNewestVersionOfEachAgentByName(t *testing.T) {
	c, store := catalogWith(t, agent(keeperID, keeperName), agent(captainID, "Ferry captain"))
	changed := agent(keeperID, keeperName)
	changed.Persona = "You keep the lamp lit."
	publish(t, store, changed)

	got := available(t, c, workspace)
	if len(got) != 2 || got[0].Name != "Ferry captain" || got[1].Name != keeperName || got[1].Number != 2 {
		t.Errorf("Available = %+v, want the ferry captain, then version 2 of the %s", got, keeperName)
	}
}

func TestAPublishIsAvailableWithoutARestart(t *testing.T) {
	c, store := catalogWith(t)
	if got := available(t, c, workspace); len(got) != 0 {
		t.Fatalf("Available before publishing = %+v, want none", got)
	}
	publish(t, store, agent(keeperID, keeperName))

	if got := available(t, c, workspace); len(got) != 1 {
		t.Errorf("Available after publishing = %+v, want the new agent", got)
	}
}

func TestAnotherWorkspaceSeesNoAgents(t *testing.T) {
	c, _ := catalogWith(t, agent(keeperID, keeperName))
	if got := available(t, c, elsewhere); len(got) != 0 {
		t.Errorf("Available(another workspace) = %+v, want none", got)
	}
}

func TestAnExactVersionIsReadOnceThenServedFromMemory(t *testing.T) {
	c, store := catalogWith(t, agent(keeperID, keeperName))
	for range 3 {
		if v, err := c.Version(context.Background(), workspace, keeperID, 1); err != nil || v.Name != keeperName {
			t.Fatalf("Version = %+v, %v", v, err)
		}
	}
	if store.reads != 1 {
		t.Errorf("store read %d times, want once: a version never changes", store.reads)
	}
}

func TestAVersionInMemoryIsServedOnlyForItsOwnNumber(t *testing.T) {
	c, store := catalogWith(t, agent(keeperID, keeperName))
	if _, err := c.Version(context.Background(), workspace, keeperID, 1); err != nil {
		t.Fatalf("Version 1: %v", err)
	}
	changed := agent(keeperID, keeperName)
	changed.Persona = "You keep the lamp lit."
	publish(t, store, changed)

	for _, want := range []agents.Version{{Agent: changed, Number: 2}, {Agent: agent(keeperID, keeperName), Number: 1}} {
		if v, err := c.Version(context.Background(), workspace, keeperID, want.Number); err != nil || v.Number != want.Number || v.Persona != want.Persona {
			t.Errorf("Version %d = %+v, %v; want its own persona, %q", want.Number, v, err, want.Persona)
		}
	}
}

func TestAVersionInMemoryIsOutOfAnotherWorkspacesReach(t *testing.T) {
	c, _ := catalogWith(t, agent(keeperID, keeperName))
	if _, err := c.Version(context.Background(), workspace, keeperID, 1); err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v, err := c.Version(context.Background(), elsewhere, keeperID, 1); !errors.Is(err, agents.ErrNotFound) {
		t.Errorf("Version from another workspace = %+v, %v; want ErrNotFound", v, err)
	}
}

func TestAVersionNobodyPublishedIsNotFound(t *testing.T) {
	c, _ := catalogWith(t, agent(keeperID, keeperName))
	tests := map[string]func() error{
		"a later number": func() error { _, err := c.Version(context.Background(), workspace, keeperID, 2); return err },
		"another workspace": func() error {
			_, err := c.Version(context.Background(), elsewhere, keeperID, 1)
			return err
		},
	}
	for name, read := range tests {
		t.Run(name, func(t *testing.T) {
			if err := read(); !errors.Is(err, agents.ErrNotFound) {
				t.Errorf("Version = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestNoCallerCanChangeAnAgentsContextKinds(t *testing.T) {
	c, _ := catalogWith(t, agent(keeperID, keeperName))
	available(t, c, workspace)[0].Reads[0] = "changed by a caller"
	v, _ := c.Version(context.Background(), workspace, keeperID, 1)
	v.Reads[0] = "changed by another caller"

	if got := available(t, c, workspace)[0].Reads; got[0] != "profile" {
		t.Errorf("newest Reads = %v, want [profile]", got)
	}
	if v, _ := c.Version(context.Background(), workspace, keeperID, 1); v.Reads[0] != "profile" {
		t.Errorf("cached Reads = %v, want [profile]", v.Reads)
	}
}

func TestAnAgentReadingAKindUserContextDoesntHoldIsRefused(t *testing.T) {
	known := []string{"profile", "session_summary"}
	keeper := agents.Agent{ID: keeperID, Name: "Lighthouse keeper", Reads: []string{"profile", "session_summary"}, Persona: "p"}
	if err := agents.CheckReads([]agents.Agent{keeper}, known); err != nil {
		t.Errorf("CheckReads refused known kinds: %v", err)
	}
	keeper.Reads = []string{"profile", "diary"}
	if err := agents.CheckReads([]agents.Agent{keeper}, known); err == nil || !strings.Contains(err.Error(), `"diary"`) {
		t.Errorf("CheckReads = %v, want a refusal naming the unknown kind", err)
	}
}
