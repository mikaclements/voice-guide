package postgres_test

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/pgtest"
	"github.com/mikaclements/voice-guide/internal/productkey"
	"github.com/mikaclements/voice-guide/internal/productkey/store/postgres"
)

var (
	workspace = uuid.MustParse("01a1de8a-0000-7000-8000-000000000001")
	elsewhere = uuid.MustParse("01a1de8a-0000-7000-8000-000000000002")
)

// keysOn returns the keys kept on a fresh database with two workspaces.
func keysOn(t *testing.T) *productkey.Keys {
	t.Helper()
	pool := pgtest.New(t)
	pgtest.AddWorkspaces(t, pool, workspace, elsewhere)
	return productkey.NewKeys(postgres.New(pool))
}

func TestAKeyWorksUntilItIsRevoked(t *testing.T) {
	ctx := context.Background()
	keys := keysOn(t)
	s, k, err := keys.Create(ctx, workspace)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ws, err := keys.Workspace(ctx, s); err != nil || ws != workspace {
		t.Fatalf("Workspace = %v, %v; want %v", ws, err, workspace)
	}
	for range 2 { // revoking again changes nothing
		if found, working, err := keys.Revoke(ctx, workspace, k.ID); err != nil || !found || working != 0 {
			t.Fatalf("Revoke = %v, %d working, %v; want found, none working", found, working, err)
		}
	}
	if _, err := keys.Workspace(ctx, s); !errors.Is(err, productkey.ErrUnknown) {
		t.Errorf("revoked key: Workspace err = %v; want ErrUnknown", err)
	}
}

func TestEachWorkspacesKeyNamesItsOwnWorkspace(t *testing.T) {
	ctx := context.Background()
	keys := keysOn(t)
	mine, _, err1 := keys.Create(ctx, workspace)
	theirs, _, err2 := keys.Create(ctx, elsewhere)
	if err := errors.Join(err1, err2); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for s, want := range map[productkey.Key]uuid.UUID{mine: workspace, theirs: elsewhere} {
		if got, err := keys.Workspace(ctx, s); err != nil || got != want {
			t.Errorf("Workspace = %v, %v; want %v", got, err, want)
		}
	}
}

func TestAWorkspaceListsCountsAndRevokesOnlyItsOwnKeys(t *testing.T) {
	ctx := context.Background()
	keys := keysOn(t)
	_, mine, _ := keys.Create(ctx, workspace)
	_, newer, _ := keys.Create(ctx, workspace)
	_, theirs, _ := keys.Create(ctx, elsewhere)
	listed, err := keys.List(ctx, workspace)
	if err != nil || len(listed) != 2 || listed[0].ID != newer.ID || listed[1].ID != mine.ID {
		t.Fatalf("List = %v, %v; want the workspace's two keys, newest first", listed, err)
	}
	if !listed[0].RevokedAt.IsZero() || listed[0].Display != newer.Display {
		t.Errorf("listed %+v; want it working, with its display", listed[0])
	}
	if found, _, err := keys.Revoke(ctx, workspace, theirs.ID); err != nil || found {
		t.Errorf("Revoke another workspace's key = %v, %v; want not found", found, err)
	}
	if found, working, err := keys.Revoke(ctx, workspace, mine.ID); err != nil || !found || working != 1 {
		t.Errorf("Revoke = %v, %d working, %v; want found, the newer key still working", found, working, err)
	}
}
