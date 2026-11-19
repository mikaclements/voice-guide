package usercontext_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/usercontext"
	"github.com/mikaclements/voice-guide/internal/usercontext/store/fake"
)

var (
	workspace = uuid.MustParse("01a1de8a-0000-7000-8000-000000000001")
	sailor    = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000c1")
	// keeper is the agent that wrote the summaries in these tests, and reads them.
	keeper = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000b1")
)

// itemsOf builds the sailor's items in the workspace, oldest first, each kind and content as given.
func itemsOf(kindsAndContents ...string) (*usercontext.Service, *fake.Items, []uuid.UUID) {
	store := fake.New()
	var ids []uuid.UUID
	for i := 0; i < len(kindsAndContents); i += 2 {
		id := uuid.NewV7()
		it := usercontext.Item{ID: id, Workspace: workspace, User: sailor, Kind: kindsAndContents[i], Content: kindsAndContents[i+1]}
		if it.Kind == usercontext.KindSessionSummary {
			it.Agent, it.Session = keeper, uuid.NewV7()
		}
		store.Add(it)
		ids = append(ids, id)
	}
	return usercontext.NewService(store), store, ids
}

func load(t *testing.T, svc *usercontext.Service, maxChars int, kinds ...string) usercontext.Loaded {
	t.Helper()
	got, err := svc.Load(context.Background(), usercontext.LoadRequest{Workspace: workspace, User: sailor, Agent: keeper, Kinds: kinds, MaxChars: maxChars})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return got
}

func contents(items []usercontext.Item) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.Content)
	}
	return out
}

func TestOnlyTheRequestedKindsLoadInTheirOrderNewestFirst(t *testing.T) {
	svc, _, _ := itemsOf(
		usercontext.KindSessionSummary, "Practised knots.",
		usercontext.KindProfile, "Likes the night watch.",
		usercontext.KindSessionSummary, "Practised tides.",
	)
	got := contents(load(t, svc, 1000, usercontext.KindProfile, usercontext.KindSessionSummary).Items)
	if want := []string{"Likes the night watch.", "Practised tides.", "Practised knots."}; !slices.Equal(got, want) {
		t.Errorf("loaded %q, want %q: the profile, then the summaries newest first", got, want)
	}
	if got := contents(load(t, svc, 1000, usercontext.KindSessionSummary).Items); !slices.Equal(got, []string{"Practised tides.", "Practised knots."}) {
		t.Errorf("loaded %q, want the summaries only, never the profile", got)
	}
}

func TestAnItemThatDoesntFitIsLeftOutAndAShorterOneStillLoads(t *testing.T) {
	svc, _, ids := itemsOf(
		usercontext.KindSessionSummary, "Short.",
		usercontext.KindSessionSummary, strings.Repeat("Long watch. ", 10),
	)
	got := load(t, svc, 10, usercontext.KindSessionSummary)
	if c := contents(got.Items); !slices.Equal(c, []string{"Short."}) || !slices.Equal(got.LeftOut, []uuid.UUID{ids[1]}) {
		t.Errorf("loaded %q, left out %v; want the short one loaded and the long one's ID left out", c, got.LeftOut)
	}
}

func TestTheLimitCoversEverythingLoadedNotEachItemAlone(t *testing.T) {
	svc, _, ids := itemsOf(
		usercontext.KindSessionSummary, "Knots.",
		usercontext.KindSessionSummary, "Tides.",
	)
	got := load(t, svc, 10, usercontext.KindSessionSummary)
	if c := contents(got.Items); !slices.Equal(c, []string{"Tides."}) || !slices.Equal(got.LeftOut, []uuid.UUID{ids[0]}) {
		t.Errorf("loaded %q, left out %v; want the newest only, since both together pass the limit", c, got.LeftOut)
	}
}

func TestTheLimitCountsCharactersNotBytes(t *testing.T) {
	// Six characters, eighteen bytes in UTF-8.
	svc, _, _ := itemsOf(usercontext.KindProfile, "夜の見張り番")
	if got := load(t, svc, 6, usercontext.KindProfile); len(got.Items) != 1 || len(got.LeftOut) != 0 {
		t.Errorf("loaded %q, left out %v; an item exactly at the limit fits", contents(got.Items), got.LeftOut)
	}
}

func TestOneCharacterOverTheLimitIsLeftOut(t *testing.T) {
	// The limit is 5 characters: the first case's item has 6; the second's newest item takes 3
	// of the 5, leaving 2 for the older one's 3.
	tests := map[string][]string{
		"an item alone":                   {usercontext.KindProfile, "夜の見張り番"},
		"an item after the ones that fit": {usercontext.KindProfile, "見張り", usercontext.KindProfile, "灯台番"},
	}
	for name, items := range tests {
		t.Run(name, func(t *testing.T) {
			svc, _, ids := itemsOf(items...)
			got := load(t, svc, 5, usercontext.KindProfile)
			if !slices.Equal(got.LeftOut, ids[:1]) || len(got.Items) != len(ids)-1 {
				t.Errorf("loaded %q, left out %v; want the oldest item, %v, left out", contents(got.Items), got.LeftOut, ids[0])
			}
		})
	}
}

func TestAReplacedItemIsNotLoaded(t *testing.T) {
	svc, store, ids := itemsOf(usercontext.KindSessionSummary, "Practised knots.")
	original, _ := store.ByID(context.Background(), workspace, sailor, ids[:1])
	store.Add(usercontext.Item{ID: uuid.NewV7(), Workspace: workspace, User: sailor, Kind: usercontext.KindSessionSummary, Content: "Practised knots and tides.",
		Replaces: ids[0], Agent: keeper, Session: original[0].Session, SessionEnded: original[0].SessionEnded})

	if got := contents(load(t, svc, 1000, usercontext.KindSessionSummary).Items); !slices.Equal(got, []string{"Practised knots and tides."}) {
		t.Errorf("loaded %q, want only the replacement", got)
	}
}

func TestItemsPastTheRowLimitAreNeitherReadNorListedAsLeftOut(t *testing.T) {
	var pairs []string
	for range 25 { // past the 20 rows a small limit reads
		pairs = append(pairs, usercontext.KindSessionSummary, "Practised knots.")
	}
	svc, _, ids := itemsOf(pairs...)
	got := load(t, svc, 16, usercontext.KindSessionSummary) // the newest fits; 19 more are read and left out
	if len(got.Items) != 1 || got.Items[0].ID != ids[24] || len(got.LeftOut) != 19 || slices.Contains(got.LeftOut, ids[0]) {
		t.Errorf("loaded %d, left out %d; want the newest loaded and the next 19 left out, the oldest five not read", len(got.Items), len(got.LeftOut))
	}
}

func TestARequestUserContextCantAnswerIsRefused(t *testing.T) {
	svc, _, _ := itemsOf()
	tests := map[string]usercontext.LoadRequest{
		"no workspace":           {User: sailor, Kinds: []string{usercontext.KindProfile}, MaxChars: 10},
		"no user":                {Workspace: workspace, Kinds: []string{usercontext.KindProfile}, MaxChars: 10},
		"a zero limit":           {Workspace: workspace, User: sailor, Kinds: []string{usercontext.KindProfile}},
		"an unknown kind":        {Workspace: workspace, User: sailor, Kinds: []string{"diary"}, MaxChars: 10},
		"summaries for no agent": {Workspace: workspace, User: sailor, Kinds: []string{usercontext.KindSessionSummary}, MaxChars: 10},
	}
	for name, r := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.Load(context.Background(), r); !errors.Is(err, usercontext.ErrBadRequest) {
				t.Errorf("Load = %v, want ErrBadRequest", err)
			}
		})
	}
}
