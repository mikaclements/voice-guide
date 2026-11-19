package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func env(overrides map[string]string) func(string) string {
	base := map[string]string{
		"DATABASE_URL":       "postgres://localhost/harbour",
		"VOICE_WORKSPACE_ID": "01a1de8b-bba7-7000-bad0-e92247a30f0f",
	}
	for k, v := range overrides {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestPublishReadsTheAgentsFolderByDefault(t *testing.T) {
	cfg, err := loadConfig(env(nil))
	if err != nil || cfg.agentsDir != "agents" {
		t.Errorf("loadConfig = %+v, %v; want the agents folder", cfg, err)
	}
}

func TestPublishRefusesToStartWithoutItsSettings(t *testing.T) {
	tests := map[string]map[string]string{
		"no database":                   {"DATABASE_URL": ""},
		"no workspace":                  {"VOICE_WORKSPACE_ID": ""},
		"a workspace that isn't a UUID": {"VOICE_WORKSPACE_ID": "harbour"},
	}
	for name, overrides := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := loadConfig(env(overrides)); err == nil {
				t.Error("loadConfig accepted missing settings")
			}
		})
	}
}

// agentsFolder returns a folder holding one agent file, reading reads.
func agentsFolder(t *testing.T, reads string) string {
	t.Helper()
	dir := t.TempDir()
	file := "---\nid: 01a1de8b-bba7-7000-bad0-0000000000a1\nname: Lighthouse keeper\ndescription: Talks ships through the fog.\nreads: [" + reads + "]\n---\nYou answer every call from the sea.\n"
	if err := os.WriteFile(filepath.Join(dir, "lighthouse-keeper.md"), []byte(file), 0o600); err != nil {
		t.Fatalf("write the agent file: %v", err)
	}
	return dir
}

// stalledDatabase returns the address of a database that takes connections
// and never answers, as one that has hung does.
func stalledDatabase(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var held []net.Conn
	var accepting sync.WaitGroup
	accepting.Go(func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
		}
	})
	t.Cleanup(func() {
		_ = l.Close()
		accepting.Wait()
		for _, c := range held {
			_ = c.Close()
		}
	})
	return "postgres://keeper@" + l.Addr().String() + "/harbour?sslmode=disable"
}

func TestPublishGivesUpOnADatabaseThatNeverAnswers(t *testing.T) {
	getenv := env(map[string]string{"DATABASE_URL": stalledDatabase(t), "VOICE_AGENTS_DIR": agentsFolder(t, "profile")})
	done := make(chan error, 1)
	go func() { done <- run(context.Background(), getenv, slog.New(slog.DiscardHandler), 100*time.Millisecond) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("run = %v, want it to give up at its deadline", err)
		}
	case <-time.After(10 * time.Second): // far past the deadline, so only a publish with none gets here
		t.Fatal("publish still waiting on the database after 10s, want it to give up at its deadline")
	}
}

// A kind nothing writes would fail every session start with the agent, so
// publishing refuses it before reaching the database: here, one no one
// listens on.
func TestPublishRefusesAnAgentReadingAKindUserContextDoesntHold(t *testing.T) {
	getenv := env(map[string]string{"DATABASE_URL": "postgres://keeper@127.0.0.1:1/harbour?sslmode=disable", "VOICE_AGENTS_DIR": agentsFolder(t, "profile, diary")})
	if err := run(context.Background(), getenv, slog.New(slog.DiscardHandler), publishTimeout); err == nil || !strings.Contains(err.Error(), `reads "diary"`) {
		t.Errorf("run = %v, want a refusal naming the unknown kind", err)
	}
}
