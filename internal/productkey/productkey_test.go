package productkey_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/productkey"
	"github.com/mikaclements/voice-guide/internal/productkey/store/fake"
)

var (
	workspace = uuid.MustParse("01a1de8a-0000-7000-8000-000000000001")
	elsewhere = uuid.MustParse("01a1de8a-0000-7000-8000-000000000002")
)

func create(t *testing.T, keys *productkey.Keys, ws uuid.UUID) (productkey.Key, productkey.Record) {
	t.Helper()
	s, k, err := keys.Create(context.Background(), ws)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return s, k
}

func TestAKeyNamesTheWorkspaceItWasMadeFor(t *testing.T) {
	keys := productkey.NewKeys(fake.New())
	mine, _ := create(t, keys, workspace)
	theirs, _ := create(t, keys, elsewhere)
	for s, want := range map[productkey.Key]uuid.UUID{mine: workspace, theirs: elsewhere} {
		if got, err := keys.Workspace(context.Background(), s); err != nil || got != want {
			t.Errorf("Workspace = %v, %v; want %v", got, err, want)
		}
	}
}

func TestOnlyTheKeysHashAndFirstCharactersAreKept(t *testing.T) {
	store := fake.New()
	s, _ := create(t, productkey.NewKeys(store), workspace)
	raw := s.Reveal()
	listed, err := store.List(context.Background(), workspace)
	if err != nil || len(listed) != 1 {
		t.Fatalf("List = %v, %v; want the one key", listed, err)
	}
	k := listed[0]
	if len(k.Hash) != 32 || strings.Contains(string(k.Hash), raw) {
		t.Errorf("kept hash %x; want a 32-byte hash, not the key", k.Hash)
	}
	if !strings.HasPrefix(raw, k.Display) || len(k.Display) >= len(raw)/2 {
		t.Errorf("display %q; want the key's first characters, far short of the key", k.Display)
	}
}

// holder keeps a key in a field fmt and slog can't call its methods on.
type holder struct{ key productkey.Key }

func TestAKeyPrintsLogsAndEncodesRedacted(t *testing.T) {
	k, _ := create(t, productkey.NewKeys(fake.New()), workspace)
	var asJSON, asText bytes.Buffer
	slog.New(slog.NewJSONHandler(&asJSON, nil)).Info("key", "key", k, "held", holder{k})
	slog.New(slog.NewTextHandler(&asText, nil)).Info("key", "key", k, "held", holder{k})
	encoded, err := json.Marshal(struct{ Key productkey.Key }{k})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for name, out := range map[string]string{
		"%v": fmt.Sprintf("%v", k), "Sprint": fmt.Sprint(k), "%q": fmt.Sprintf("%q", k), "%#v": fmt.Sprintf("%#v", k),
		"%d": fmt.Sprintf("%d", k), "%x": fmt.Sprintf("%x", k),
		"%+v of an exported field":   fmt.Sprintf("%+v", struct{ Key productkey.Key }{k}),
		"%+v of an unexported field": fmt.Sprintf("%+v", holder{k}),
		"%#v of an unexported field": fmt.Sprintf("%#v", holder{k}),
		"slog's JSON handler":        asJSON.String(),
		"slog's text handler":        asText.String(),
		"JSON":                       string(encoded),
	} {
		if strings.Contains(out, k.Reveal()) {
			t.Errorf("%s shows the key: %s", name, out)
		}
	}
}

func TestEveryKeyIsNew(t *testing.T) {
	keys := productkey.NewKeys(fake.New())
	first, _ := create(t, keys, workspace)
	second, _ := create(t, keys, workspace)
	if first.Reveal() == second.Reveal() {
		t.Error("two keys are the same")
	}
}

func TestAKeyThatDoesntWorkIsUnknownWithoutSayingWhy(t *testing.T) {
	keys := productkey.NewKeys(fake.New())
	revoked, k := create(t, keys, workspace)
	if found, _, err := keys.Revoke(context.Background(), workspace, k.ID); err != nil || !found {
		t.Fatalf("Revoke = %v, %v", found, err)
	}
	s, _ := create(t, keys, workspace)
	live := s.Reveal()
	last := live[len(live)-1]
	other := byte('A')
	if last == other {
		other = 'B'
	}
	for name, raw := range map[string]string{
		"revoked":         revoked.Reveal(),
		"never made":      "vgk_" + strings.Repeat("A", 43),
		"one letter off":  live[:len(live)-1] + string(other),
		"too short":       live[:len(live)-1],
		"no prefix":       strings.TrimPrefix(live, "vgk_"),
		"not base64url":   "vgk_" + strings.Repeat("+", 43),
		"a signed ticket": "eyJhbGciOiJIUzI1NiJ9.e30.c2ln",
		"empty":           "",
	} {
		if _, err := keys.Workspace(context.Background(), productkey.NewKey(raw)); !errors.Is(err, productkey.ErrUnknown) {
			t.Errorf("%s: Workspace err = %v; want ErrUnknown", name, err)
		}
	}
}

func TestAMalformedKeyIsRefusedWithoutAStoreRead(t *testing.T) {
	store := fake.New()
	if _, err := productkey.NewKeys(store).Workspace(context.Background(), productkey.NewKey("vgk_short")); !errors.Is(err, productkey.ErrUnknown) {
		t.Fatalf("Workspace err = %v; want ErrUnknown", err)
	}
	if store.Reads() != 0 {
		t.Errorf("%d store reads; want none for a malformed key", store.Reads())
	}
}

func TestAKeyCheckThatCantReachTheStoreIsNotUnknown(t *testing.T) {
	store := fake.New()
	keys := productkey.NewKeys(store)
	s, _ := create(t, keys, workspace)
	store.Fail(errors.New("database down"))
	_, err := keys.Workspace(context.Background(), s)
	if err == nil || errors.Is(err, productkey.ErrUnknown) {
		t.Errorf("Workspace err = %v; want a failure, not ErrUnknown, so the product isn't told its key is wrong", err)
	}
}

func TestRevokingAnotherWorkspacesKeyFindsNothing(t *testing.T) {
	keys := productkey.NewKeys(fake.New())
	s, k := create(t, keys, workspace)
	if found, _, err := keys.Revoke(context.Background(), elsewhere, k.ID); err != nil || found {
		t.Errorf("Revoke from another workspace = %v, %v; want not found", found, err)
	}
	if _, err := keys.Workspace(context.Background(), s); err != nil {
		t.Errorf("the key stopped working: %v", err)
	}
}

func TestRevokingSaysHowManyKeysStillWork(t *testing.T) {
	keys := productkey.NewKeys(fake.New())
	_, old := create(t, keys, workspace)
	_, current := create(t, keys, workspace)
	create(t, keys, elsewhere)
	for _, tt := range []struct {
		record  productkey.Record
		working int
	}{{old, 1}, {old, 1}, {current, 0}} {
		if found, working, err := keys.Revoke(context.Background(), workspace, tt.record.ID); err != nil || !found || working != tt.working {
			t.Errorf("Revoke = %v, %d working, %v; want found, %d working", found, working, err, tt.working)
		}
	}
}

func TestLooksTellsAKeyFromATicket(t *testing.T) {
	s, _ := create(t, productkey.NewKeys(fake.New()), workspace)
	if !productkey.Looks(s.Reveal()) || productkey.Looks("eyJhbGciOiJIUzI1NiJ9.e30.c2ln") {
		t.Error("Looks doesn't tell a key from a ticket")
	}
}
