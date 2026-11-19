package postgres_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/pgtest"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	"github.com/mikaclements/voice-guide/internal/usercontext/store/postgres"
)

var (
	workspace = uuid.MustParse("01a1de8a-0000-7000-8000-000000000001")
	elsewhere = uuid.MustParse("01a1de8a-0000-7000-8000-000000000002")
	sailor    = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000c1")
	navigator = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000c2")
	// keeper is the agent that wrote the summaries in these tests, and reads them.
	keeper = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000b1")
)

// storeWithTwoUsers returns a store on a fresh database with two workspaces and two users, and the pool beneath it.
func storeWithTwoUsers(t *testing.T) (*postgres.Items, *pgxpool.Pool) {
	t.Helper()
	pool := pgtest.New(t)
	pgtest.AddWorkspaces(t, pool, workspace, elsewhere)
	pgtest.Exec(t, pool, "INSERT INTO users (id) VALUES ($1), ($2)", sailor, navigator)
	return postgres.New(pool), pool
}

// add saves an item and returns its ID; replaces is the nil UUID for a first item.
func add(t *testing.T, pool *pgxpool.Pool, ws, user uuid.UUID, kind, content string, replaces uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.NewV7()
	if err := insert(pool, id, ws, user, kind, content, replaces); err != nil {
		t.Fatalf("insert %s: %v", kind, err)
	}
	return id
}

func insert(pool *pgxpool.Pool, id, ws, user uuid.UUID, kind, content string, replaces uuid.UUID) error {
	_, err := pool.Exec(context.Background(),
		`INSERT INTO user_context.context_items (id, workspace_id, user_id, kind, content, replaces, agent_id, source_session_id, source_ended_at)
		 SELECT $1, $2, $3, $4::text, $5, NULLIF($6::uuid, '00000000-0000-0000-0000-000000000000'),
		        CASE WHEN $4::text = 'session_summary' THEN $7::uuid END,
		        -- A summary is of a session of its own; a correction is of its original's.
		        CASE WHEN $4::text = 'session_summary' THEN coalesce(original.source_session_id, uuidv7()) END,
		        CASE WHEN $4::text = 'session_summary' THEN coalesce(original.source_ended_at, clock_timestamp()) END
		 FROM (SELECT 1) one LEFT JOIN user_context.context_items original ON original.id = $6::uuid`,
		id, ws, user, kind, content, replaces, keeper)
	return err
}

func current(t *testing.T, s *postgres.Items, ws, user uuid.UUID, kinds ...string) []usercontext.Item {
	t.Helper()
	got, err := s.Current(context.Background(), ws, user, kinds, keeper, 20)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	return got
}

func contents(items []usercontext.Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Content)
	}
	return out
}

func TestCurrentReturnsOnlyTheNamedKindsNewestFirst(t *testing.T) {
	s, pool := storeWithTwoUsers(t)
	add(t, pool, workspace, sailor, usercontext.KindSessionSummary, "Practised knots.", uuid.Nil())
	add(t, pool, workspace, sailor, usercontext.KindProfile, "Likes the night watch.", uuid.Nil())
	add(t, pool, workspace, sailor, usercontext.KindSessionSummary, "Practised tides.", uuid.Nil())

	got := current(t, s, workspace, sailor, usercontext.KindSessionSummary)
	if c := contents(got); len(c) != 2 || c[0] != "Practised tides." || c[1] != "Practised knots." {
		t.Errorf("Current = %q, want the two summaries, newest first", c)
	}
	if got[0].Workspace != workspace || got[0].User != sailor || got[0].Kind != usercontext.KindSessionSummary || got[0].CreatedAt.IsZero() {
		t.Errorf("item = %+v, want every field read back", got[0])
	}
}

func TestAReplacedItemIsNoLongerCurrent(t *testing.T) {
	s, pool := storeWithTwoUsers(t)
	first := add(t, pool, workspace, sailor, usercontext.KindSessionSummary, "Deckhand, two seasons.", uuid.Nil())
	add(t, pool, workspace, sailor, usercontext.KindSessionSummary, "Bosun, three seasons.", first)

	if c := contents(current(t, s, workspace, sailor, usercontext.KindSessionSummary)); len(c) != 1 || c[0] != "Bosun, three seasons." {
		t.Errorf("Current = %q, want only the replacement", c)
	}
}

func TestAnotherWorkspacesOrUsersItemsAreOutOfReach(t *testing.T) {
	s, pool := storeWithTwoUsers(t)
	add(t, pool, elsewhere, sailor, usercontext.KindSessionSummary, "Written for the ferry port.", uuid.Nil())
	add(t, pool, workspace, navigator, usercontext.KindSessionSummary, "The navigator's summary.", uuid.Nil())

	if got := current(t, s, workspace, sailor, usercontext.KindSessionSummary); len(got) != 0 {
		t.Errorf("Current = %q, want nothing of another workspace's or user's", contents(got))
	}
}

// The order asked for is the core's rule, tested there; the store returns the items in any order.
func TestByIDReturnsTheNamedItemsReplacedOrNot(t *testing.T) {
	s, pool := storeWithTwoUsers(t)
	first := add(t, pool, workspace, sailor, usercontext.KindSessionSummary, "Deckhand, two seasons.", uuid.Nil())
	add(t, pool, workspace, sailor, usercontext.KindSessionSummary, "Bosun, three seasons.", first)
	profile := add(t, pool, workspace, sailor, usercontext.KindProfile, "Likes the night watch.", uuid.Nil())
	summary := add(t, pool, workspace, sailor, usercontext.KindSessionSummary, "Practised knots.", uuid.Nil())

	got, err := s.ByID(context.Background(), workspace, sailor, []uuid.UUID{summary, first, profile})
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	c := contents(got)
	slices.Sort(c)
	if want := []string{"Deckhand, two seasons.", "Likes the night watch.", "Practised knots."}; !slices.Equal(c, want) {
		t.Errorf("ByID = %q, want %q: the named items, the replaced one included", c, want)
	}
}

func TestItemsRefusesAnIDOutsideTheUsersItemsInTheWorkspace(t *testing.T) {
	s, pool := storeWithTwoUsers(t)
	mine := add(t, pool, workspace, sailor, usercontext.KindProfile, "Likes the night watch.", uuid.Nil())
	tests := map[string]uuid.UUID{
		"another workspace's": add(t, pool, elsewhere, sailor, usercontext.KindProfile, "Written for the ferry port.", uuid.Nil()),
		"another user's":      add(t, pool, workspace, navigator, usercontext.KindProfile, "Likes charts.", uuid.Nil()),
		"one never saved":     uuid.NewV7(),
	}
	for name, id := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := usercontext.NewService(s).Items(context.Background(), usercontext.ItemsRequest{Workspace: workspace, User: sailor, IDs: []uuid.UUID{mine, id}})
			if !errors.Is(err, usercontext.ErrNoSuchItem) {
				t.Errorf("Items = %v, want ErrNoSuchItem", err)
			}
		})
	}
}

func TestTheTableRefusesWhatAnItemMayNotBe(t *testing.T) {
	_, pool := storeWithTwoUsers(t)
	first := add(t, pool, workspace, sailor, usercontext.KindSessionSummary, "Deckhand, two seasons.", uuid.Nil())
	add(t, pool, workspace, sailor, usercontext.KindSessionSummary, "Bosun, three seasons.", first)
	profile := add(t, pool, workspace, sailor, usercontext.KindProfile, "Likes the night watch.", uuid.Nil())
	// What refuses each: its SQLSTATE and, for a constraint, the constraint's name.
	const check, unique, foreignKey, restrict = "23514", "23505", "23503", "23001"
	const replacesFK = "context_items_replaces_workspace_id_user_id_kind_fkey"
	tests := map[string]struct {
		try              func() error
		code, constraint string
	}{
		"an unknown kind": {func() error {
			return insert(pool, uuid.NewV7(), workspace, sailor, "diary", "Saw a whale.", uuid.Nil())
		}, check, "context_items_kind_check"},
		"a document, which a product sends as session context": {func() error {
			return insert(pool, uuid.NewV7(), workspace, sailor, "document", "Deckhand, two seasons.", uuid.Nil())
		}, check, "context_items_kind_check"},
		"an ID that isn't a UUIDv7": {func() error {
			return insert(pool, uuid.NewV4(), workspace, sailor, usercontext.KindProfile, "Likes the night watch.", uuid.Nil())
		}, check, "context_items_id_check"},
		"empty content": {func() error {
			return insert(pool, uuid.NewV7(), workspace, sailor, usercontext.KindProfile, "", uuid.Nil())
		}, check, "context_items_content_check"},
		"a second replacement of one item": {func() error {
			return insert(pool, uuid.NewV7(), workspace, sailor, usercontext.KindSessionSummary, "Mate, four seasons.", first)
		}, unique, "context_items_replaces_key"},
		"replacing another kind": {func() error {
			return insert(pool, uuid.NewV7(), workspace, sailor, usercontext.KindSessionSummary, "Bosun.", profile)
		}, foreignKey, replacesFK},
		"replacing another user's item": {func() error {
			return insert(pool, uuid.NewV7(), workspace, navigator, usercontext.KindProfile, "Likes charts.", profile)
		}, foreignKey, replacesFK},
		"an edit in place": {func() error {
			_, err := pool.Exec(context.Background(), "UPDATE user_context.context_items SET content = 'Captain.' WHERE id = $1", first)
			return err
		}, restrict, ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var pgErr *pgconn.PgError
			if err := tt.try(); !errors.As(err, &pgErr) || pgErr.Code != tt.code || pgErr.ConstraintName != tt.constraint {
				t.Errorf("error = %v, want PostgreSQL to refuse it with %s %q", err, tt.code, tt.constraint)
			}
		})
	}
}

func TestAReplacementReadsBackTheItemItReplaced(t *testing.T) {
	s, pool := storeWithTwoUsers(t)
	first := add(t, pool, workspace, sailor, usercontext.KindProfile, "Likes the night watch.", uuid.Nil())
	second := add(t, pool, workspace, sailor, usercontext.KindProfile, "Likes the dawn watch.", first)

	got, err := s.ByID(context.Background(), workspace, sailor, []uuid.UUID{first, second})
	if err != nil || len(got) != 2 {
		t.Fatalf("ByID = %+v, %v; want both items", got, err)
	}
	for _, it := range got {
		want := uuid.Nil()
		if it.ID == second {
			want = first
		}
		if it.Replaces != want {
			t.Errorf("item %s replaces %s, want %s", it.Content, it.Replaces, want)
		}
	}
	current, err := s.Current(context.Background(), workspace, sailor, []string{usercontext.KindProfile}, keeper, 20)
	if err != nil || len(current) != 1 || current[0].Replaces != first {
		t.Errorf("Current = %+v, %v; want the replacement, saying what it replaced", current, err)
	}
}
