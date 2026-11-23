package postgres_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/conversation/store/postgres"
	"github.com/mikaclements/voice-guide/internal/pgtest"
)

// pilotID is a second agent in the first workspace.
var pilotID = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a3")

// withPilot adds the harbour pilot, at version 1, to the first workspace.
func withPilot(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	pgtest.Exec(t, pool, "INSERT INTO agent.agents (id, workspace_id) VALUES ($1, $2)", pilotID, workspace)
	pgtest.Exec(t, pool, `INSERT INTO agent.agent_versions (workspace_id, agent_id, version, name, description, reads, persona, content_hash)
		VALUES ($1, $2, 1, 'Harbour pilot', '', '{}', 'You bring ships in.', 'hash')`, workspace, pilotID)
}

func ended(t *testing.T, s *postgres.Sessions, sess conversation.Session) bool {
	t.Helper()
	got, found, err := s.ByID(context.Background(), sess.Workspace, sess.User, sess.ID)
	if err != nil || !found {
		t.Fatalf("ByID = %v, %v", found, err)
	}
	return got.Ended()
}

func TestANewSessionEndsOnlyTheUsersOtherLiveSessionsWithItsAgentQueuingTheirSummaries(t *testing.T) {
	s, pool := storeWithTwoWorkspaces(t)
	withPilot(t, pool)
	old, _ := start(t, s, sessionOn(workspace, user), uuid.NewV7())
	pilots := sessionOn(workspace, user)
	pilots.Agent = pilotID
	start(t, s, pilots, uuid.NewV7())
	theirs, _ := start(t, s, sessionOn(workspace, uuid.NewV7()), uuid.NewV7())
	elsewheres, _ := start(t, s, sessionOn(elsewhere, user), uuid.NewV7())

	started, isNew := start(t, s, sessionOn(workspace, user), uuid.NewV7())
	if !isNew || !ended(t, s, old) || ended(t, s, started) || summaryJobs(t, pool) != 1 {
		t.Errorf("after a new start: the earlier session ended %v, the new one %v, %d summary jobs; want the earlier ended with its summary queued, the new one live",
			ended(t, s, old), ended(t, s, started), summaryJobs(t, pool))
	}
	for name, other := range map[string]conversation.Session{"with another agent": pilots, "another user's": theirs, "in another workspace": elsewheres} {
		if ended(t, s, other) {
			t.Errorf("a session %s ended, want it live", name)
		}
	}
}

func TestARepeatedStartEndsNothing(t *testing.T) {
	s, pool := storeWithTwoWorkspaces(t)
	request := uuid.NewV7()
	first, _ := start(t, s, sessionOn(workspace, user), request)
	later, _ := start(t, s, sessionOn(workspace, user), uuid.NewV7()) // ends the first

	if again, isNew := start(t, s, sessionOn(workspace, user), request); isNew || again.ID != first.ID || ended(t, s, later) || summaryJobs(t, pool) != 1 {
		t.Errorf("repeat = %+v, new %v, the later session ended %v, %d summary jobs; want the first session back and the later one still live",
			again, isNew, ended(t, s, later), summaryJobs(t, pool))
	}
}

func TestStartsAtOnceForOneUserAndAgentLeaveOneSessionLive(t *testing.T) {
	s, pool := storeWithTwoWorkspaces(t)
	const starts = 8
	var wg sync.WaitGroup
	for range starts {
		wg.Go(func() {
			if _, _, err := s.Start(context.Background(), sessionOn(workspace, user), uuid.NewV7(), ""); err != nil {
				t.Errorf("Start: %v", err)
			}
		})
	}
	wg.Wait()
	var live int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM sessions WHERE ended_at IS NULL").Scan(&live); err != nil {
		t.Fatalf("count the live sessions: %v", err)
	}
	if live != 1 || summaryJobs(t, pool) != starts-1 {
		t.Errorf("%d sessions live and %d summary jobs after %d starts at once, want one live and every other ended", live, summaryJobs(t, pool), starts)
	}
}

func TestUnsummarizedFindsTheUsersLiveAndPendingSessionsWithTheAgentOldestFirst(t *testing.T) {
	s, pool := storeWithTwoWorkspaces(t)
	withPilot(t, pool)
	ctx := context.Background()
	settled, _ := start(t, s, sessionOn(workspace, user), uuid.NewV7())
	pending, _ := start(t, s, sessionOn(workspace, user), uuid.NewV7()) // ends the first
	if err := s.SettleSummary(ctx, workspace, user, settled.ID, conversation.SummaryWritten); err != nil {
		t.Fatalf("SettleSummary: %v", err)
	}
	live, _ := start(t, s, sessionOn(workspace, user), uuid.NewV7()) // ends the second
	pilots := sessionOn(workspace, user)
	pilots.Agent = pilotID
	start(t, s, pilots, uuid.NewV7())
	start(t, s, sessionOn(workspace, uuid.NewV7()), uuid.NewV7())
	start(t, s, sessionOn(elsewhere, user), uuid.NewV7())

	got, err := s.Unsummarized(ctx, workspace, user, keeperID)
	if err != nil || !slices.Equal(got, []uuid.UUID{pending.ID, live.ID}) {
		t.Errorf("Unsummarized = %v, %v; want the pending session then the live one, %v and %v", got, err, pending.ID, live.ID)
	}
}

func TestTheSessionsASessionCarriesAreSavedInOrderAndReadBack(t *testing.T) {
	s, _ := storeWithTwoWorkspaces(t)
	carried := []uuid.UUID{uuid.NewV7(), uuid.NewV7()}
	sess := sessionOn(workspace, user)
	sess.CarriedSessions = carried
	start(t, s, sess, uuid.NewV7())

	got, _, err := s.ByID(context.Background(), workspace, user, sess.ID)
	if err != nil || !slices.Equal(got.CarriedSessions, carried) {
		t.Errorf("carried sessions read back = %v, %v; want %v", got.CarriedSessions, err, carried)
	}
}

func TestFinishedOfReadsTheUsersFinishedTurnsOfTheSessionsInOrderInTheirWorkspace(t *testing.T) {
	s, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	take := func(sess conversation.Session, words string) {
		turn := turnOf(sess, words)
		startTurn(t, turns, turn)
		finish(t, turns, &turn, conversation.TurnAnswered, "Aye.")
	}
	first, _ := start(t, s, sessionOn(workspace, user), uuid.NewV7())
	take(first, "Is the lamp lit?")
	take(first, "And the fog horn?")
	second, _ := start(t, s, sessionOn(workspace, user), uuid.NewV7())
	take(second, "Where is the ferry?")
	startTurn(t, turns, turnOf(second, "Still there?")) // in flight
	third, _ := start(t, s, sessionOn(workspace, user), uuid.NewV7())
	take(third, "Not carried.")

	got, err := turns.FinishedOf(context.Background(), workspace, user, []uuid.UUID{second.ID, first.ID})
	var words []string
	for _, turn := range got {
		words = append(words, turn.Words)
	}
	if want := []string{"Is the lamp lit?", "And the fog horn?", "Where is the ferry?"}; err != nil || !slices.Equal(words, want) {
		t.Errorf("FinishedOf = %v, %v; want %v: the first session's, then the second's, none in flight", words, err, want)
	}
	if got, _ := turns.FinishedOf(context.Background(), workspace, uuid.NewV7(), []uuid.UUID{first.ID}); len(got) != 0 {
		t.Errorf("another user reads %v, want nothing", got)
	}
	if got, _ := turns.FinishedOf(context.Background(), elsewhere, user, []uuid.UUID{first.ID}); len(got) != 0 {
		t.Errorf("another workspace reads %v, want nothing", got)
	}
}
