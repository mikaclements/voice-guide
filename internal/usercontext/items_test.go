package usercontext_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/usercontext"
)

func TestItemsComeBackInTheOrderAskedForReplacedOrNot(t *testing.T) {
	svc, store, ids := itemsOf(
		usercontext.KindSessionSummary, "Deckhand, two seasons.",
		usercontext.KindProfile, "Likes the night watch.",
		usercontext.KindSessionSummary, "Practised knots.",
	)
	store.Add(usercontext.Item{ID: uuid.NewV7(), Workspace: workspace, User: sailor, Kind: usercontext.KindSessionSummary, Content: "Bosun, three seasons.", Replaces: ids[0]})

	got, err := svc.Items(context.Background(), usercontext.ItemsRequest{Workspace: workspace, User: sailor, IDs: []uuid.UUID{ids[2], ids[0], ids[1]}})
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if c, want := contents(got), []string{"Practised knots.", "Deckhand, two seasons.", "Likes the night watch."}; !slices.Equal(c, want) {
		t.Errorf("Items = %q, want %q: the order asked for, the replaced item included", c, want)
	}
}

func TestItemsRefusesAnIDOutsideTheUsersItemsInTheWorkspace(t *testing.T) {
	svc, store, ids := itemsOf(usercontext.KindProfile, "Likes the night watch.")
	add := func(ws, user uuid.UUID, content string) uuid.UUID {
		id := uuid.NewV7()
		store.Add(usercontext.Item{ID: id, Workspace: ws, User: user, Kind: usercontext.KindProfile, Content: content})
		return id
	}
	tests := map[string]uuid.UUID{
		"another workspace's": add(uuid.NewV7(), sailor, "Written for the ferry port."),
		"another user's":      add(workspace, uuid.NewV7(), "Likes charts."),
		"one never saved":     uuid.NewV7(),
	}
	for name, id := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Items(context.Background(), usercontext.ItemsRequest{Workspace: workspace, User: sailor, IDs: []uuid.UUID{ids[0], id}})
			if !errors.Is(err, usercontext.ErrNoSuchItem) {
				t.Errorf("Items = %v, want ErrNoSuchItem", err)
			}
		})
	}
}
