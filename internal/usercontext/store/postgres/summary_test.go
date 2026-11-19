package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/usercontext"
)

// captain is another agent of the workspace.
var captain = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000b2")

func summaryOf(session, agent uuid.UUID, content string) usercontext.Item {
	return usercontext.Item{ID: uuid.NewV7(), Workspace: workspace, User: sailor, Kind: usercontext.KindSessionSummary, Content: content, Agent: agent, Session: session, SessionEnded: time.Now()}
}

func TestASessionHasOneSummaryHoweverManySavesRaceForIt(t *testing.T) {
	s, _ := storeWithTwoUsers(t)
	session := uuid.NewV7()
	var saves sync.WaitGroup
	var mu sync.Mutex
	var firsts, ids []uuid.UUID
	for range 8 {
		saves.Go(func() {
			got, isNew, err := s.AddSummary(context.Background(), summaryOf(session, keeper, "Covered knots."))
			if err != nil {
				t.Errorf("AddSummary: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			ids = append(ids, got.ID)
			if isNew {
				firsts = append(firsts, got.ID)
			}
		})
	}
	saves.Wait()
	if len(firsts) != 1 {
		t.Fatalf("%d saves were the first, want one", len(firsts))
	}
	for _, id := range ids {
		if id != firsts[0] {
			t.Errorf("a save returned %s, want the session's one summary, %s", id, firsts[0])
		}
	}
	got := current(t, s, workspace, sailor, usercontext.KindSessionSummary)
	if len(got) != 1 || got[0].Agent != keeper || got[0].Session != session {
		t.Errorf("Current = %+v, want the one summary with its agent and session", got)
	}
}

func TestAnAgentReadsOnlyTheSummariesItWroteAndEveryoneTheProfile(t *testing.T) {
	s, pool := storeWithTwoUsers(t)
	if _, _, err := s.AddSummary(context.Background(), summaryOf(uuid.NewV7(), keeper, "Covered knots.")); err != nil {
		t.Fatalf("AddSummary: %v", err)
	}
	if _, _, err := s.AddSummary(context.Background(), summaryOf(uuid.NewV7(), captain, "Covered the crossing.")); err != nil {
		t.Fatalf("AddSummary: %v", err)
	}
	add(t, pool, workspace, sailor, usercontext.KindProfile, "Likes the night watch.", uuid.Nil())
	for agent, want := range map[uuid.UUID][]string{keeper: {"Covered knots.", "Likes the night watch."}, captain: {"Covered the crossing.", "Likes the night watch."}} {
		got, err := s.Current(context.Background(), workspace, sailor, []string{usercontext.KindSessionSummary, usercontext.KindProfile}, agent, 20)
		if c := contents(got); err != nil || len(c) != 2 || !containsAll(c, want) {
			t.Errorf("agent %s read %q, %v; want %q", agent, c, err, want)
		}
	}
}

func TestSummariesComeNewestSessionFirstWhateverOrderTheyWereSavedIn(t *testing.T) {
	s, _ := storeWithTwoUsers(t)
	ctx := context.Background()
	later, earlier := summaryOf(uuid.NewV7(), keeper, "Covered the fog."), summaryOf(uuid.NewV7(), keeper, "Covered the lamp.")
	earlier.SessionEnded = later.SessionEnded.Add(-time.Hour)
	for _, it := range []usercontext.Item{later, earlier} { // the earlier session's saved last
		if _, _, err := s.AddSummary(ctx, it); err != nil {
			t.Fatalf("AddSummary: %v", err)
		}
	}
	got := current(t, s, workspace, sailor, usercontext.KindSessionSummary)
	if c := contents(got); len(c) != 2 || c[0] != "Covered the fog." || !got[0].SessionEnded.Equal(later.SessionEnded.Truncate(time.Microsecond)) {
		t.Errorf("Current = %+v, want the latest session's summary first, with when its session ended", got)
	}
}

func TestTheTableRefusesASummaryWithoutItsAgentAndSession(t *testing.T) {
	_, pool := storeWithTwoUsers(t)
	_, err := pool.Exec(context.Background(), `INSERT INTO user_context.context_items (id, workspace_id, user_id, kind, content)
		VALUES ($1, $2, $3, 'session_summary', 'Covered knots.')`, uuid.NewV7(), workspace, sailor)
	if err == nil {
		t.Error("a summary with no agent or session was saved")
	}
}

func containsAll(got, want []string) bool {
	for _, w := range want {
		found := false
		for _, g := range got {
			found = found || g == w
		}
		if !found {
			return false
		}
	}
	return true
}

func TestASaveRacingACorrectionReturnsTheOriginal(t *testing.T) {
	s, pool := storeWithTwoUsers(t)
	ctx := context.Background()
	session := uuid.NewV7()
	first, _, err := s.AddSummary(ctx, summaryOf(session, keeper, "Covered knots."))
	if err != nil {
		t.Fatalf("AddSummary: %v", err)
	}
	add(t, pool, workspace, sailor, usercontext.KindSessionSummary, "Covered knots and tides.", first.ID) // a correction
	again, isNew, err := s.AddSummary(ctx, summaryOf(session, keeper, "A second run's."))
	if err != nil || isNew || again.ID != first.ID {
		t.Errorf("AddSummary again = %+v, %v, %v; want the original returned, nothing saved", again, isNew, err)
	}
	if got := contents(current(t, s, workspace, sailor, usercontext.KindSessionSummary)); len(got) != 1 || got[0] != "Covered knots and tides." {
		t.Errorf("current summaries %q, want only the correction", got)
	}
}

func TestASessionsSummaryReadsBackAsItStandsOnlyForItsOwnUserAndWorkspace(t *testing.T) {
	s, pool := storeWithTwoUsers(t)
	ctx := context.Background()
	session := uuid.NewV7()
	if _, found, err := s.SessionSummary(ctx, workspace, sailor, session); found || err != nil {
		t.Fatalf("SessionSummary before any = %v, %v; want none", found, err)
	}
	first, _, err := s.AddSummary(ctx, summaryOf(session, keeper, "Covered knots."))
	if err != nil {
		t.Fatalf("AddSummary: %v", err)
	}
	if _, _, err := s.AddSummary(ctx, summaryOf(uuid.NewV7(), keeper, "Another session's.")); err != nil {
		t.Fatalf("AddSummary: %v", err)
	}
	if got, found, err := s.SessionSummary(ctx, workspace, sailor, session); !found || err != nil || got.ID != first.ID {
		t.Errorf("SessionSummary = %+v, %v, %v; want the session's own summary", got, found, err)
	}
	add(t, pool, workspace, sailor, usercontext.KindSessionSummary, "Covered knots and tides.", first.ID) // a correction
	if got, _, _ := s.SessionSummary(ctx, workspace, sailor, session); got.Content != "Covered knots and tides." {
		t.Errorf("SessionSummary after a correction = %q, want the correction", got.Content)
	}
	for name, by := range map[string][2]uuid.UUID{"another workspace": {elsewhere, sailor}, "another user": {workspace, navigator}} {
		if got, found, err := s.SessionSummary(ctx, by[0], by[1], session); found || err != nil {
			t.Errorf("%s: SessionSummary = %+v, %v, %v; want none", name, got, found, err)
		}
	}
}

func TestAtMostTheLimitOfEachKindIsReadNewestFirst(t *testing.T) {
	s, pool := storeWithTwoUsers(t)
	ctx := context.Background()
	add(t, pool, workspace, sailor, usercontext.KindProfile, "Likes the night watch.", uuid.Nil())
	for i := range 3 {
		it := summaryOf(uuid.NewV7(), keeper, []string{"Oldest.", "Older.", "Newest."}[i])
		it.SessionEnded = time.Now().Add(time.Duration(i) * time.Minute)
		if _, _, err := s.AddSummary(ctx, it); err != nil {
			t.Fatalf("AddSummary: %v", err)
		}
	}
	got, err := s.Current(ctx, workspace, sailor, []string{usercontext.KindSessionSummary, usercontext.KindProfile}, keeper, 2)
	if c := contents(got); err != nil || len(c) != 3 || !containsAll(c, []string{"Newest.", "Older.", "Likes the night watch."}) {
		t.Errorf("Current = %q, %v; want the two newest summaries and the profile, each kind held to the limit", c, err)
	}
}
