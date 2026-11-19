package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/conversation/store/fake"
)

var workspace = uuid.MustParse("01a1de8a-0000-7000-8000-000000000001")

func do(t *testing.T, webhooks conversation.Webhooks, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	err := command(context.Background(), webhooks, workspace, args, &out)
	return out.String(), err
}

func secretIn(out string) string {
	for line := range strings.Lines(out) {
		if strings.HasPrefix(line, "whsec_") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func TestSettingTheURLShowsTheSecretOnlyTheFirstTime(t *testing.T) {
	webhooks := fake.NewWebhooks()
	first, err := do(t, webhooks, "set", "https://jobs.example/webhooks")
	if err != nil || secretIn(first) == "" {
		t.Fatalf("set = %q, %v; want the secret shown", first, err)
	}
	again, err := do(t, webhooks, "set", "https://jobs.example/hooks/v2")
	if err != nil || secretIn(again) != "" || !strings.Contains(again, "unchanged") {
		t.Errorf("set again = %q, %v; want the secret kept and not shown", again, err)
	}
	if shown, err := do(t, webhooks, "show"); err != nil || strings.TrimSpace(shown) != "https://jobs.example/hooks/v2" {
		t.Errorf("show = %q, %v; want the URL alone, never the secret", shown, err)
	}
}

func TestRotatingShowsANewSecret(t *testing.T) {
	webhooks := fake.NewWebhooks()
	set, _ := do(t, webhooks, "set", "https://jobs.example/webhooks")
	rotated, err := do(t, webhooks, "rotate")
	if err != nil || secretIn(rotated) == "" || secretIn(rotated) == secretIn(set) {
		t.Errorf("rotate = %q, %v; want a new secret shown", rotated, err)
	}
}

func TestShowSaysWhenDeliveriesAreStopped(t *testing.T) {
	webhooks := fake.NewWebhooks()
	_, _ = do(t, webhooks, "set", "https://jobs.example/webhooks")
	_ = webhooks.Stop(context.Background(), workspace, "https://jobs.example/webhooks")
	if shown, err := do(t, webhooks, "show"); err != nil || !strings.Contains(shown, "stopped") {
		t.Errorf("show = %q, %v; want it said deliveries are stopped", shown, err)
	}
}

func TestAWorkspaceWithNoWebhookHasNothingToShowOrRotate(t *testing.T) {
	for _, args := range [][]string{{"show"}, {"rotate"}} {
		if out, err := do(t, fake.NewWebhooks(), args...); !errors.Is(err, conversation.ErrNoWebhook) || out != "" {
			t.Errorf("%s = %q, %v; want ErrNoWebhook", args[0], out, err)
		}
	}
}

func TestABadURLOrCommandIsRefused(t *testing.T) {
	for _, args := range [][]string{{"set", "jobs.example/webhooks"}, {"set"}, {"remove"}, {}} {
		if out, err := do(t, fake.NewWebhooks(), args...); err == nil || out != "" {
			t.Errorf("%q = %q, %v; want refused, nothing set", args, out, err)
		}
	}
}
