package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/agents/store/postgres"
	"github.com/mikaclements/voice-guide/internal/pgtest"
)

var (
	workspace = uuid.MustParse("01a1de8a-0000-7000-8000-000000000001")
	elsewhere = uuid.MustParse("01a1de8a-0000-7000-8000-000000000002")
	keeperID  = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a1")
	captainID = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a2")
)

// agentNamed builds a valid agent with the given ID and name.
func agentNamed(id uuid.UUID, name string) agents.Agent {
	return agents.Agent{ID: id, Name: name, Description: "Works the harbour.", Reads: []string{"profile"}, Persona: "You answer every call from the sea."}
}

// storeWithWorkspaces returns a store on a fresh database holding the two test workspaces.
func storeWithWorkspaces(t *testing.T) *postgres.Versions {
	t.Helper()
	pool := pgtest.New(t)
	for _, id := range []uuid.UUID{workspace, elsewhere} {
		if _, err := pool.Exec(context.Background(), "INSERT INTO workspaces (id, name) VALUES ($1, 'Harbour')", id); err != nil {
			t.Fatalf("add workspace: %v", err)
		}
	}
	return postgres.New(pool)
}

func save(t *testing.T, s *postgres.Versions, a agents.Agent, hash string) (int, bool) {
	t.Helper()
	n, saved, err := s.Save(context.Background(), workspace, a, hash)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	return n, saved
}

func TestASavedVersionReadsBackAsSaved(t *testing.T) {
	s := storeWithWorkspaces(t)
	keeper := agentNamed(keeperID, "Lighthouse keeper")
	if n, saved := save(t, s, keeper, "first"); n != 1 || !saved {
		t.Fatalf("Save = %d, %v; want 1, true", n, saved)
	}

	got, err := s.Version(context.Background(), workspace, keeperID, 1)
	if err != nil || got.Number != 1 || got.Name != keeper.Name || got.Description != keeper.Description ||
		got.Persona != keeper.Persona || !slices.Equal(got.Reads, keeper.Reads) {
		t.Errorf("Version = %+v, %v; want %+v as version 1", got, err, keeper)
	}
}

func TestTheSessionContextReadsBackInOrder(t *testing.T) {
	s := storeWithWorkspaces(t)
	keeper := agentNamed(keeperID, "Lighthouse keeper")
	keeper.SessionContext = []string{"resume", "current_page"}
	save(t, s, keeper, "first")

	got, err := s.Version(context.Background(), workspace, keeperID, 1)
	if err != nil || !slices.Equal(got.SessionContext, keeper.SessionContext) {
		t.Errorf("Version = %+v, %v; want the session context %v", got, err, keeper.SessionContext)
	}
	newest, err := s.Newest(context.Background(), workspace)
	if err != nil || len(newest) != 1 || !slices.Equal(newest[0].SessionContext, keeper.SessionContext) {
		t.Errorf("Newest = %+v, %v; want the session context there too", newest, err)
	}
}

func TestTheSummaryInstructionReadsBack(t *testing.T) {
	s := storeWithWorkspaces(t)
	keeper := agentNamed(keeperID, "Lighthouse keeper")
	keeper.Summary = "Note the ships you guided."
	save(t, s, keeper, "first")

	got, err := s.Version(context.Background(), workspace, keeperID, 1)
	if err != nil || got.Summary != keeper.Summary {
		t.Errorf("Version = %+v, %v; want the summary instruction %q", got, err, keeper.Summary)
	}
	newest, err := s.Newest(context.Background(), workspace)
	if err != nil || len(newest) != 1 || newest[0].Summary != keeper.Summary {
		t.Errorf("Newest = %+v, %v; want the summary instruction there too", newest, err)
	}
}

func TestAnAgentThatOpensReadsBackAsOpening(t *testing.T) {
	s := storeWithWorkspaces(t)
	keeper := agentNamed(keeperID, "Lighthouse keeper")
	keeper.Opens = true
	save(t, s, keeper, "first")

	got, err := s.Version(context.Background(), workspace, keeperID, 1)
	newest, newestErr := s.Newest(context.Background(), workspace)
	if err != nil || newestErr != nil || !got.Opens || len(newest) != 1 || !newest[0].Opens {
		t.Errorf("Version = %+v, %v; Newest = %+v, %v; want both opening", got, err, newest, newestErr)
	}
}

func TestAnAgentsModelReadsBackAndOneWithoutReadsBackEmpty(t *testing.T) {
	s := storeWithWorkspaces(t)
	keeper := agentNamed(keeperID, "Lighthouse keeper")
	save(t, s, keeper, "first")
	keeper.Model = "claude-sonnet-5-5"
	save(t, s, keeper, "second")

	first, err := s.Version(context.Background(), workspace, keeperID, 1)
	second, secondErr := s.Version(context.Background(), workspace, keeperID, 2)
	newest, newestErr := s.Newest(context.Background(), workspace)
	if err != nil || secondErr != nil || newestErr != nil || first.Model != "" || second.Model != keeper.Model || len(newest) != 1 || newest[0].Model != keeper.Model {
		t.Errorf("versions %+v, %v and %+v, %v; Newest %+v, %v; want no model, then the model named", first, err, second, secondErr, newest, newestErr)
	}
}

func TestAnAgentsVoiceAndSpeedReadBackAndOneWithoutReadsBackEmpty(t *testing.T) {
	s := storeWithWorkspaces(t)
	keeper := agentNamed(keeperID, "Lighthouse keeper")
	save(t, s, keeper, "first")
	keeper.Voice, keeper.Speed = "bf_emma", 1.1
	save(t, s, keeper, "second")

	first, err := s.Version(context.Background(), workspace, keeperID, 1)
	second, secondErr := s.Version(context.Background(), workspace, keeperID, 2)
	newest, newestErr := s.Newest(context.Background(), workspace)
	if err != nil || secondErr != nil || newestErr != nil || first.Voice != "" || first.Speed != 0 ||
		second.Voice != "bf_emma" || second.Speed != 1.1 || len(newest) != 1 || newest[0].Voice != "bf_emma" || newest[0].Speed != 1.1 {
		t.Errorf("versions %+v, %v and %+v, %v; Newest %+v, %v; want nothing set, then bf_emma at 1.1 exactly", first, err, second, secondErr, newest, newestErr)
	}
}

func TestAnAgentsSummaryModelAndSchemaReadBackExactlyAndOneWithoutReadsBackEmpty(t *testing.T) {
	s := storeWithWorkspaces(t)
	keeper := agentNamed(keeperID, "Lighthouse keeper")
	keeper.Summary = "Note the ships you guided."
	save(t, s, keeper, "first")
	keeper.SummaryModel = "claude-opus-5-5"
	// As the loader encodes it: keys sorted, no spaces. PostgreSQL keeps jsonb in an order and
	// spacing of its own, and a number past a float's precision must still read back as written.
	keeper.SummarySchema = json.RawMessage(`{"properties":{"score":{"maximum":12345678901234567890,"minimum":1,"type":"integer"}},"required":["score"],"type":"object"}`)
	save(t, s, keeper, "second")

	first, err := s.Version(context.Background(), workspace, keeperID, 1)
	if err != nil || first.SummaryModel != "" || first.SummarySchema != nil {
		t.Errorf("version 1 = %+v, %v; want no summary model and no schema", first, err)
	}
	second, err := s.Version(context.Background(), workspace, keeperID, 2)
	if err != nil || second.SummaryModel != "claude-opus-5-5" || string(second.SummarySchema) != string(keeper.SummarySchema) {
		t.Errorf("version 2 = %+v, %s, %v; want the summary model and %s exactly", second, second.SummarySchema, err, keeper.SummarySchema)
	}
	newest, err := s.Newest(context.Background(), workspace)
	if err != nil || len(newest) != 1 || newest[0].SummaryModel != "claude-opus-5-5" || string(newest[0].SummarySchema) != string(keeper.SummarySchema) {
		t.Errorf("Newest = %+v, %v; want the summary model and %s exactly", newest, err, keeper.SummarySchema)
	}
}

func TestTheSameContentAgainSavesNoVersion(t *testing.T) {
	s := storeWithWorkspaces(t)
	save(t, s, agentNamed(keeperID, "Lighthouse keeper"), "first")
	if n, saved := save(t, s, agentNamed(keeperID, "Lighthouse keeper"), "first"); n != 1 || saved {
		t.Errorf("Save again = %d, %v; want 1, false", n, saved)
	}
}

func TestChangedContentSavesTheNextVersionAndKeepsTheOld(t *testing.T) {
	s := storeWithWorkspaces(t)
	save(t, s, agentNamed(keeperID, "Lighthouse keeper"), "first")
	changed := agentNamed(keeperID, "Harbour keeper")
	if n, saved := save(t, s, changed, "second"); n != 2 || !saved {
		t.Fatalf("Save changed = %d, %v; want 2, true", n, saved)
	}

	newest, err := s.Newest(context.Background(), workspace)
	if err != nil || len(newest) != 1 || newest[0].Number != 2 || newest[0].Name != "Harbour keeper" {
		t.Errorf("Newest = %+v, %v; want only version 2", newest, err)
	}
	if old, err := s.Version(context.Background(), workspace, keeperID, 1); err != nil || old.Name != "Lighthouse keeper" {
		t.Errorf("version 1 = %+v, %v; want it unchanged", old, err)
	}
}

func TestNewestListsTheWorkspacesAgentsByName(t *testing.T) {
	s := storeWithWorkspaces(t)
	save(t, s, agentNamed(keeperID, "Lighthouse keeper"), "keeper")
	save(t, s, agentNamed(captainID, "Ferry captain"), "captain")

	got, err := s.Newest(context.Background(), workspace)
	if err != nil || len(got) != 2 || got[0].ID != captainID || got[1].ID != keeperID {
		t.Errorf("Newest = %+v, %v; want the ferry captain, then the lighthouse keeper", got, err)
	}
}

func TestSavesOfOneAgentAtOnceNeverShareANumber(t *testing.T) {
	s := storeWithWorkspaces(t)
	const saves = 8
	numbers := make([]int, saves)
	errs := make([]error, saves)
	var wg sync.WaitGroup
	for i := range saves {
		wg.Go(func() {
			numbers[i], _, errs[i] = s.Save(context.Background(), workspace, agentNamed(keeperID, "Lighthouse keeper"), string(rune('a'+i)))
		})
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Fatalf("Save: %v", err)
	}
	slices.Sort(numbers)
	if !slices.Equal(numbers, []int{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Errorf("numbers given = %v, want each of 1 to 8 once", numbers)
	}
}

func TestAnotherWorkspacesAgentsAreOutOfReach(t *testing.T) {
	s := storeWithWorkspaces(t)
	save(t, s, agentNamed(keeperID, "Lighthouse keeper"), "first")
	ctx := context.Background()

	if got, err := s.Newest(ctx, elsewhere); err != nil || len(got) != 0 {
		t.Errorf("Newest(elsewhere) = %+v, %v; want none", got, err)
	}
	if _, err := s.Version(ctx, elsewhere, keeperID, 1); !errors.Is(err, agents.ErrNotFound) {
		t.Errorf("Version(elsewhere) = %v, want ErrNotFound", err)
	}
	if _, _, err := s.Save(ctx, elsewhere, agentNamed(keeperID, "Lighthouse keeper"), "other"); !errors.Is(err, agents.ErrAgentInAnotherWorkspace) {
		t.Errorf("Save(elsewhere) = %v, want ErrAgentInAnotherWorkspace", err)
	}
}

func TestSavingToAWorkspaceThatDoesntExistSaysSo(t *testing.T) {
	s := storeWithWorkspaces(t)
	missing := uuid.MustParse("01a1de8a-0000-7000-8000-000000000009")

	_, _, err := s.Save(context.Background(), missing, agentNamed(keeperID, "Lighthouse keeper"), "first")
	if err == nil || !strings.Contains(err.Error(), "doesn't exist") {
		t.Errorf("Save = %v, want an error naming the missing workspace", err)
	}
}
