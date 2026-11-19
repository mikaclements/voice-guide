package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/conversation/store/postgres"
	"github.com/mikaclements/voice-guide/internal/pgtest"
)

// deadline is how long a turn in flight holds up its session in these tests.
const deadline = time.Minute

func turnOf(s conversation.Session, words string) conversation.Turn {
	return conversation.Turn{ID: uuid.NewV7(), Workspace: s.Workspace, User: s.User, Session: s.ID, RequestID: uuid.NewV7(), Words: words}
}

// startTurn starts the turn after the session's last finished turn, as a
// turn whose prompt was just built does.
func startTurn(t *testing.T, turns *postgres.Turns, turn conversation.Turn) int {
	t.Helper()
	finished, err := turns.Finished(context.Background(), turn.Workspace, turn.Session)
	if err != nil {
		t.Fatalf("Finished: %v", err)
	}
	seq, err := turns.Start(context.Background(), turn, len(finished), deadline)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return seq
}

func finish(t *testing.T, turns *postgres.Turns, turn *conversation.Turn, status conversation.TurnStatus, reply string) {
	t.Helper()
	turn.Status, turn.Reply = status, reply
	if err := turns.Finish(context.Background(), *turn); err != nil {
		t.Fatalf("Finish: %v", err)
	}
}

func TestTurnsAreNumberedFromOneInEachSession(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	one, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	two, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())

	var got []int
	for _, s := range []conversation.Session{one, one, two} {
		turn := turnOf(s, "Is the lamp lit?")
		got = append(got, startTurn(t, turns, turn))
		finish(t, turns, &turn, conversation.TurnAnswered, "It is.")
	}
	if !slices.Equal(got, []int{1, 2, 1}) {
		t.Errorf("numbers = %v, want 1 and 2 in the first session, then 1 in the second", got)
	}
}

func TestATurnInFlightHoldsUpItsSessionUntilTheDeadline(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	startTurn(t, turns, turnOf(s, "Is the lamp lit?"))
	if _, err := turns.Start(context.Background(), turnOf(s, "And the fog?"), 0, deadline); !errors.Is(err, conversation.ErrTurnInFlight) {
		t.Fatalf("Start during the first turn = %v, want ErrTurnInFlight", err)
	}

	// The instance serving the first turn stopped without finishing it.
	pgtest.Exec(t, pool, "UPDATE turns SET created_at = created_at - interval '61 seconds'")
	if seq, err := turns.Start(context.Background(), turnOf(s, "And the fog?"), 0, deadline); err != nil || seq != 2 {
		t.Errorf("Start after the deadline = %d, %v; want turn 2", seq, err)
	}
}

func TestATurnCutOffIsSavedLostWhenTheNextStartsSoItsWordsReachLaterPrompts(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	cut := turnOf(s, "Is the lamp lit?")
	startTurn(t, turns, cut)
	pgtest.Exec(t, pool, "UPDATE turns SET created_at = created_at - interval '61 seconds'")
	startTurn(t, turns, turnOf(s, "And the fog?"))

	got, err := turns.Finished(context.Background(), workspace, s.ID)
	want := []conversation.Turn{{Workspace: workspace, Session: s.ID, RequestID: cut.RequestID, Seq: 1, ScreenInput: []conversation.ScreenInputBlock{}, Words: cut.Words, Status: conversation.TurnLost}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Finished = %+v, %v; want the turn cut off, lost with its words", got, err)
	}
	cut.Status, cut.Reply = conversation.TurnAnswered, "It is."
	if err := turns.Finish(context.Background(), cut); err == nil {
		t.Error("Finish saved a reply to a turn cut off")
	}
}

// What the session's next turn was built on is checked in the statement that
// saves it: a turn that finished since the prompt was read refuses it.
func TestATurnIsRefusedOnceATurnItsPromptLacksHasFinished(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	first := turnOf(s, "Is the lamp lit?")
	startTurn(t, turns, first)
	finish(t, turns, &first, conversation.TurnAnswered, "It is.")
	if _, err := turns.Start(context.Background(), turnOf(s, "And the fog?"), 0, deadline); !errors.Is(err, conversation.ErrTurnInFlight) {
		t.Errorf("Start built on no turns = %v, want ErrTurnInFlight", err)
	}
	if seq, err := turns.Start(context.Background(), turnOf(s, "And the fog?"), 1, deadline); err != nil || seq != 2 {
		t.Errorf("Start built on turn 1 = %d, %v; want turn 2", seq, err)
	}
}

func TestATurnALaterTurnFollowedCantBeFinished(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	early := turnOf(s, "Is the lamp lit?")
	startTurn(t, turns, early)
	pgtest.Exec(t, pool, "INSERT INTO turns (id, workspace_id, user_id, session_id, request_id, seq, user_text) VALUES ($1, $2, $3, $4, $5, 2, 'And the fog?')",
		uuid.NewV7(), workspace, user, s.ID, uuid.NewV7())
	early.Status = conversation.TurnAnswered
	if err := turns.Finish(context.Background(), early); err == nil {
		t.Error("Finish saved a turn a later turn followed")
	}
}

func TestARequestIDTheSessionAlreadyUsedIsRefusedAsARepeat(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	first := turnOf(s, "Is the lamp lit?")
	startTurn(t, turns, first)
	finish(t, turns, &first, conversation.TurnAnswered, "It is.")
	again := turnOf(s, "Is the lamp lit?")
	again.RequestID = first.RequestID
	if _, err := turns.Start(context.Background(), again, 1, deadline); !errors.Is(err, conversation.ErrTurnRepeated) {
		t.Errorf("Start with the first turn's request ID = %v, want ErrTurnRepeated", err)
	}
	other, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	elsewhere := turnOf(other, "Is the lamp lit?")
	elsewhere.RequestID = first.RequestID
	startTurn(t, turns, elsewhere)
}

func TestATurnWaitingOnAnUncommittedTurnIsRefusedOnceItCommits(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "INSERT INTO turns (id, workspace_id, user_id, session_id, request_id, seq, user_text) VALUES ($1, $2, $3, $4, $5, 1, 'Is the lamp lit?')",
		uuid.NewV7(), workspace, user, s.ID, uuid.NewV7()); err != nil {
		t.Fatalf("add turn: %v", err)
	}

	refused := make(chan error, 1)
	go func() {
		_, err := turns.Start(ctx, turnOf(s, "And the fog?"), 0, deadline)
		refused <- err
	}()
	waitForABlockedQuery(t, pool)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := <-refused; !errors.Is(err, conversation.ErrTurnInFlight) {
		t.Errorf("the waiting Start = %v, want ErrTurnInFlight", err)
	}
}

func openingOf(s conversation.Session) conversation.Turn {
	turn := turnOf(s, "")
	turn.Opening = true
	return turn
}

func TestAnOpeningIsTurnOneAndReadsBackAsTheOpening(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	open := openingOf(s)
	if seq := startTurn(t, turns, open); seq != 1 {
		t.Errorf("the opening is turn %d, want 1", seq)
	}
	finish(t, turns, &open, conversation.TurnAnswered, "Welcome aboard.")
	if got, err := turns.Finished(context.Background(), workspace, s.ID); err != nil || len(got) != 1 || !got[0].Opening || got[0].Words != "" || got[0].Reply != "Welcome aboard." {
		t.Errorf("Finished = %+v, %v; want the opening, with no words and its greeting", got, err)
	}
}

func TestAnOpeningIsRefusedOnceTheSessionHasATurnAndOneSentAgainIsARepeat(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	ctx := context.Background()
	for name, tt := range map[string]struct {
		before func(s conversation.Session) conversation.Turn
		want   error
	}{
		"a turn in flight": {func(s conversation.Session) conversation.Turn {
			startTurn(t, turns, turnOf(s, "Is the lamp lit?"))
			return openingOf(s)
		}, conversation.ErrOpeningRefused},
		"a turn cut off, in flight past the deadline": {func(s conversation.Session) conversation.Turn {
			startTurn(t, turns, turnOf(s, "Is the lamp lit?"))
			pgtest.Exec(t, pool, "UPDATE turns SET created_at = created_at - interval '61 seconds' WHERE session_id = $1", s.ID)
			return openingOf(s)
		}, conversation.ErrOpeningRefused},
		"a turn finished": {func(s conversation.Session) conversation.Turn {
			first := turnOf(s, "Is the lamp lit?")
			startTurn(t, turns, first)
			finish(t, turns, &first, conversation.TurnAnswered, "It is.")
			return openingOf(s)
		}, conversation.ErrOpeningRefused},
		"the opening taken with the same request ID": {func(s conversation.Session) conversation.Turn {
			open := openingOf(s)
			startTurn(t, turns, open)
			finish(t, turns, &open, conversation.TurnAnswered, "Welcome aboard.")
			return open
		}, conversation.ErrTurnRepeated},
		"the session ended": {func(s conversation.Session) conversation.Turn {
			if _, _, err := sessions.End(ctx, workspace, user, s.ID); err != nil {
				t.Fatalf("End: %v", err)
			}
			return openingOf(s)
		}, conversation.ErrSessionEnded},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
			open := tt.before(s)
			if _, err := turns.Start(ctx, open, 0, deadline); !errors.Is(err, tt.want) {
				t.Errorf("Start = %v, want %v", err, tt.want)
			}
		})
	}
}

// Two openings at once, from two tabs or two copies of voice: the second waits on the first's
// number, then finds it taken.
func TestAnOpeningWaitingOnAnUncommittedOpeningIsRefusedOnceItCommits(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "INSERT INTO turns (id, workspace_id, user_id, session_id, request_id, seq, user_text, opening) VALUES ($1, $2, $3, $4, $5, 1, '', true)",
		uuid.NewV7(), workspace, user, s.ID, uuid.NewV7()); err != nil {
		t.Fatalf("add the opening: %v", err)
	}

	refused := make(chan error, 1)
	go func() {
		_, err := turns.Start(ctx, openingOf(s), 0, deadline)
		refused <- err
	}()
	waitForABlockedQuery(t, pool)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := <-refused; !errors.Is(err, conversation.ErrOpeningRefused) {
		t.Errorf("the waiting opening = %v, want ErrOpeningRefused", err)
	}
}

func TestFinishedTurnsComeBackInOrderWithTheirRequestIDScreenInputAndReplyAsSaved(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	first := turnOf(s, "Where is the ferry?")
	first.ScreenInput = []conversation.ScreenInputBlock{{Name: "chart", Content: "Rocks \"north\".\n海図"}, {Name: "tide_table", Content: "High water at six."}}
	startTurn(t, turns, first)
	finish(t, turns, &first, conversation.TurnAnswered, "  **Bearing 040.**\n\n\n")
	second := turnOf(s, "Thanks.")
	startTurn(t, turns, second)
	finish(t, turns, &second, conversation.TurnLost, "")
	startTurn(t, turns, turnOf(s, "And the fog?"))

	got, err := turns.Finished(context.Background(), workspace, s.ID)
	want := []conversation.Turn{
		{Workspace: workspace, Session: s.ID, RequestID: first.RequestID, Seq: 1, ScreenInput: first.ScreenInput, Words: first.Words, Reply: first.Reply, Status: conversation.TurnAnswered},
		{Workspace: workspace, Session: s.ID, RequestID: second.RequestID, Seq: 2, ScreenInput: []conversation.ScreenInputBlock{}, Words: second.Words, Status: conversation.TurnLost},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Finished = %+v, %v; want %+v, leaving out the turn in flight", got, err, want)
	}
}

func TestTheTurnsTableRefusesARowThatContradictsItself(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	v4, now := uuid.MustParse("6f1c2c3e-8b1a-4c5d-9e7f-0a1b2c3d4e5f"), time.Now()
	for name, row := range map[string]struct {
		id       uuid.UUID
		seq      int
		status   string
		finished *time.Time
	}{
		"an ID that isn't a UUIDv7":      {v4, 1, "in_flight", nil},
		"a number below 1":               {uuid.NewV7(), 0, "in_flight", nil},
		"a status outside the four":      {uuid.NewV7(), 1, "sunk", &now},
		"in flight with a finish time":   {uuid.NewV7(), 1, "in_flight", &now},
		"answered without a finish time": {uuid.NewV7(), 1, "answered", nil},
	} {
		_, err := pool.Exec(context.Background(), `INSERT INTO turns (id, workspace_id, user_id, session_id, request_id, seq, user_text, status, finished_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'Is the lamp lit?', $7, $8)`, row.id, workspace, user, s.ID, uuid.NewV7(), row.seq, row.status, row.finished)
		if pgErr := (*pgconn.PgError)(nil); !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Errorf("%s: err = %v, want a check violation", name, err)
		}
	}
}

func TestTheTurnsTableRefusesAnOpeningAfterTurnOneOrWithWords(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	for name, row := range map[string]struct {
		seq   int
		words string
	}{"after turn 1": {2, ""}, "with words": {1, "Hello?"}} {
		_, err := pool.Exec(context.Background(), `INSERT INTO turns (id, workspace_id, user_id, session_id, request_id, seq, user_text, opening)
			VALUES ($1, $2, $3, $4, $5, $6, $7, true)`, uuid.NewV7(), workspace, user, s.ID, uuid.NewV7(), row.seq, row.words)
		if pgErr := (*pgconn.PgError)(nil); !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Errorf("an opening %s: err = %v, want a check violation", name, err)
		}
	}
}

func TestAnotherWorkspaceCantReachASessionsTurns(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	answered := turnOf(s, "Is the lamp lit?")
	startTurn(t, turns, answered)
	finish(t, turns, &answered, conversation.TurnAnswered, "It is.")
	inFlight := turnOf(s, "And the fog?")
	startTurn(t, turns, inFlight)
	ctx := context.Background()

	t.Run("starting a turn", func(t *testing.T) {
		fresh, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
		otherWorkspace, otherUser := turnOf(fresh, "Is it raining?"), turnOf(fresh, "Is it raining?")
		otherWorkspace.Workspace, otherUser.User = elsewhere, uuid.NewV7()
		for _, stray := range []conversation.Turn{otherWorkspace, otherUser} {
			if _, err := turns.Start(ctx, stray, 0, deadline); err == nil {
				t.Errorf("Start saved a turn for %v in %v, in a session of another", stray.User, stray.Workspace)
			}
		}
	})
	t.Run("finishing a turn", func(t *testing.T) {
		stray := inFlight
		stray.Workspace, stray.Status = elsewhere, conversation.TurnAnswered
		if err := turns.Finish(ctx, stray); err == nil {
			t.Error("Finish ended a turn of another workspace")
		}
		if got, _ := turns.Finished(ctx, workspace, s.ID); len(got) != 1 {
			t.Errorf("finished turns = %+v, want the turn in flight still in flight", got)
		}
	})
	t.Run("reading the turns", func(t *testing.T) {
		if got, err := turns.Finished(ctx, elsewhere, s.ID); len(got) != 0 || err != nil {
			t.Errorf("Finished from another workspace = %+v, %v; want nothing", got, err)
		}
	})
}

func TestATurnsStageTimesAreSavedWithTheReplysReport(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	turn := turnOf(s, "Is the lamp lit?")
	startTurn(t, turns, turn)
	finish(t, turns, &turn, conversation.TurnAnswered, "It is.")
	began, firstText, served := time.Now(), 610*time.Millisecond, "claude-subscription/claude-sonnet-5-5"
	turn.Stages = []conversation.Stage{
		{Name: conversation.StagePrompt, Started: began, Took: 3 * time.Millisecond},
		{Name: conversation.StageModelFirstText, Implementation: served, Started: began, Took: 640 * time.Millisecond},
		{Name: conversation.StageModelReply, Implementation: served, Started: began, Took: 1300 * time.Millisecond, Report: &conversation.ModelReport{
			Provider: "claude-subscription", Model: "claude-sonnet-5-5", Total: 1270 * time.Millisecond, FirstText: &firstText,
			Usage: &conversation.ModelUsage{Input: 12, CachedInput: 1340, CacheWrite: 58, Output: 31},
		}},
		{Name: conversation.StageSave, Started: began, Took: 4 * time.Millisecond},
	}
	if err := turns.RecordStages(context.Background(), turn); err != nil {
		t.Fatalf("RecordStages: %v", err)
	}

	rows, err := pool.Query(context.Background(), "SELECT stage, implementation, duration_ms, details::text FROM turn_stages WHERE workspace_id = $1 AND turn_id = $2 ORDER BY duration_ms", workspace, turn.ID)
	if err != nil {
		t.Fatalf("read the stages: %v", err)
	}
	var got []string
	for rows.Next() {
		var stage, impl, details string
		var ms int
		if err := rows.Scan(&stage, &impl, &ms, &details); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, fmt.Sprintf("%s %s %d %s", stage, impl, ms, details))
	}
	want := []string{
		"prompt  3 {}",
		"save  4 {}",
		"model_first_text " + served + " 640 {}",
		`model_reply ` + served + ` 1300 {"usage": {"input_tokens": 12, "output_tokens": 31, "cache_write_tokens": 58, "cached_input_tokens": 1340}, "model_total_ms": 1270, "model_first_text_ms": 610}`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("stages =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestTheStagesSpeechTimesAreSavedWithTheTurn(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	turn := turnOf(s, "Is the lamp lit?")
	startTurn(t, turns, turn)
	began := time.Now()
	names := []string{"final_text", "end_of_turn_check", "taken", "first_phrase_ready", "first_audio_sent"}
	for _, name := range names {
		turn.Stages = append(turn.Stages, conversation.Stage{Name: name, Started: began, Took: 5 * time.Millisecond})
	}
	if err := turns.RecordStages(context.Background(), turn); err != nil {
		t.Fatalf("RecordStages: %v", err)
	}
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM turn_stages WHERE workspace_id = $1 AND turn_id = $2", workspace, turn.ID).Scan(&n); err != nil || n != len(names) {
		t.Errorf("%d stages saved (%v), want speech's %d", n, err, len(names))
	}
}

func TestAStageCantClaimAnotherWorkspacesTurn(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	turn := turnOf(s, "Is the lamp lit?")
	startTurn(t, turns, turn)
	turn.Workspace = elsewhere
	turn.Stages = []conversation.Stage{{Name: conversation.StagePrompt, Started: time.Now(), Took: time.Millisecond}}
	if err := turns.RecordStages(context.Background(), turn); err == nil {
		t.Error("RecordStages saved a stage under another workspace than its turn's")
	}
}

func TestACutTurnKeepsItsWholeReplyAndComesBackHeardAsFarAsTheUserHeard(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	turn := turnOf(s, "Where to?")
	startTurn(t, turns, turn)
	reply := "Café, then the harbour."
	finish(t, turns, &turn, conversation.TurnAnswered, reply)

	turn.Status, turn.Heard = conversation.TurnCut, len("Café,")
	if err := turns.Cut(context.Background(), turn); err != nil {
		t.Fatalf("Cut: %v", err)
	}
	got, err := turns.Finished(context.Background(), workspace, s.ID)
	if err != nil || len(got) != 1 || got[0].Status != conversation.TurnCut || got[0].Reply != reply || got[0].HeardReply() != "Café," {
		t.Errorf("Finished = %+v, %v; want the whole reply kept, cut after %q", got, err, "Café,")
	}
	var fromSQL string
	if err := pool.QueryRow(context.Background(), "SELECT left(agent_text, heard_chars) FROM turns").Scan(&fromSQL); err != nil || fromSQL != "Café," {
		t.Errorf("the part heard read from SQL = %q, %v; want %q", fromSQL, err, "Café,")
	}
}

// Each refusal must come from the query's own conditions, not from a check on
// the table, which would hide a condition left out.
func TestOnlyTheLastAnsweredTurnOfItsOwnWorkspaceCanBeCut(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	ctx := context.Background()
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	followed := turnOf(s, "Is the lamp lit?")
	startTurn(t, turns, followed)
	finish(t, turns, &followed, conversation.TurnAnswered, "It is.")
	last := turnOf(s, "And the fog?")
	startTurn(t, turns, last)
	finish(t, turns, &last, conversation.TurnAnswered, "Thick tonight.")
	lost, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	failed := turnOf(lost, "Is it raining?")
	startTurn(t, turns, failed)
	finish(t, turns, &failed, conversation.TurnLost, "It is rain")
	inFlightSession, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	inFlight := turnOf(inFlightSession, "Is it windy?")
	startTurn(t, turns, inFlight)
	inFlight.Reply = "Very."

	otherWorkspace, otherReply := last, last
	otherWorkspace.Workspace = elsewhere
	otherReply.Reply = "Thin tonight."
	for name, turn := range map[string]conversation.Turn{
		"a turn a later one followed":    followed,
		"another workspace's turn":       otherWorkspace,
		"a turn the model didn't finish": failed,
		"a turn in flight":               inFlight,
		"a reply other than the saved":   otherReply,
	} {
		turn.Status, turn.Heard = conversation.TurnCut, len("Thin")
		err := turns.Cut(ctx, turn)
		if pgErr := (*pgconn.PgError)(nil); err == nil || errors.As(err, &pgErr) {
			t.Errorf("Cut %s = %v, want it refused by the query", name, err)
		}
	}

	last.Status, last.Heard = conversation.TurnCut, len("Thick")
	if err := turns.Cut(ctx, last); err != nil {
		t.Fatalf("Cut the last turn: %v", err)
	}
	last.Heard = 2
	err := turns.Cut(ctx, last)
	if pgErr := (*pgconn.PgError)(nil); err == nil || errors.As(err, &pgErr) {
		t.Errorf("Cut a cut turn again = %v, want it refused by the query", err)
	}
	if got, _ := turns.Finished(ctx, workspace, s.ID); got[1].HeardReply() != "Thick" {
		t.Errorf("the last turn heard %q, want %q, unchanged by the refused cut", got[1].HeardReply(), "Thick")
	}
}

func TestTheTurnsTableRefusesAHeardCountThatContradictsTheTurn(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	turn := turnOf(s, "Is the lamp lit?")
	startTurn(t, turns, turn)
	finish(t, turns, &turn, conversation.TurnAnswered, "It is.")
	for name, update := range map[string]string{
		"cut without a heard count":   "UPDATE turns SET status = 'cut'",
		"answered with a heard count": "UPDATE turns SET heard_chars = 1",
		"heard past the reply's end":  "UPDATE turns SET status = 'cut', heard_chars = 7",
		"heard a negative count":      "UPDATE turns SET status = 'cut', heard_chars = -1",
	} {
		_, err := pool.Exec(context.Background(), update)
		if pgErr := (*pgconn.PgError)(nil); !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Errorf("%s: err = %v, want a check violation", name, err)
		}
	}
}

func TestOnlyATurnInFlightInItsOwnWorkspaceCanBeRemoved(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	turns := postgres.NewTurns(pool)
	ctx := context.Background()
	s, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	finished := turnOf(s, "Is the lamp lit?")
	startTurn(t, turns, finished)
	finish(t, turns, &finished, conversation.TurnAnswered, "It is.")
	inFlight := turnOf(s, "And the fog?")
	startTurn(t, turns, inFlight)
	otherWorkspace := inFlight
	otherWorkspace.Workspace = elsewhere
	for name, turn := range map[string]conversation.Turn{"a finished turn": finished, "another workspace's turn": otherWorkspace} {
		err := turns.Remove(ctx, turn)
		if pgErr := (*pgconn.PgError)(nil); err == nil || errors.As(err, &pgErr) {
			t.Errorf("Remove %s = %v, want it refused by the query", name, err)
		}
	}

	if err := turns.Remove(ctx, inFlight); err != nil {
		t.Fatalf("Remove the turn in flight: %v", err)
	}
	// Its number and request ID are free again, for all the words in its place.
	if seq := startTurn(t, turns, inFlight); seq != 2 {
		t.Errorf("the turn in place of one removed took number %d, want 2", seq)
	}
}
