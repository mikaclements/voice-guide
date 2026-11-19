package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/productkey"
	"github.com/mikaclements/voice-guide/internal/productkey/store/fake"
)

var workspace = uuid.MustParse("01a1de8a-0000-7000-8000-000000000001")

// countingFails is a store of keys whose revokes work but whose count of the keys left fails.
type countingFails struct{ *fake.Keys }

func (countingFails) Working(context.Context, uuid.UUID) (int, error) {
	return 0, errors.New("connection refused")
}

func revoke(t *testing.T, keys *productkey.Keys, id uuid.UUID) (string, error) {
	t.Helper()
	var out strings.Builder
	err := command(context.Background(), keys, workspace, []string{"revoke", id.String()}, &out)
	return out.String(), err
}

func TestRevokingAKeyTheWorkspaceDoesntHaveIsAnError(t *testing.T) {
	out, err := revoke(t, productkey.NewKeys(fake.New()), uuid.NewV7())
	if err == nil || strings.Contains(out, "revoked") {
		t.Errorf("revoke = %q, %v; want an error and nothing said revoked", out, err)
	}
}

func TestRevokingTheLastWorkingKeyWarns(t *testing.T) {
	keys := productkey.NewKeys(fake.New())
	_, old, _ := keys.Create(context.Background(), workspace)
	_, last, _ := keys.Create(context.Background(), workspace)
	if out, err := revoke(t, keys, old.ID); err != nil || !strings.Contains(out, "revoked "+old.ID.String()) || strings.Contains(out, "warning") {
		t.Errorf("revoke with a key left = %q, %v; want it revoked, with no warning", out, err)
	}
	if out, err := revoke(t, keys, last.ID); err != nil || !strings.Contains(out, "revoked "+last.ID.String()) || !strings.Contains(out, "warning: the workspace has no working key left") {
		t.Errorf("revoke of the last key = %q, %v; want it revoked, with the warning", out, err)
	}
}

func TestARevokeWhoseCountFailsStillSaysItRevoked(t *testing.T) {
	store := fake.New()
	_, k, _ := productkey.NewKeys(store).Create(context.Background(), workspace)
	out, err := revoke(t, productkey.NewKeys(countingFails{store}), k.ID)
	if err == nil || !strings.Contains(out, "revoked "+k.ID.String()) {
		t.Errorf("revoke = %q, %v; want it said revoked, then the error", out, err)
	}
}
