package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
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

// The voice check itself is the agents core's, tested there; here, that the script reports a
// check it couldn't make, with the database no one listens on: the save fails, and the warning is logged all the same.
func TestPublishWarnsThatVoicesWereNotCheckedWhenTheTextToSpeechServiceCantBeAsked(t *testing.T) {
	kokoro := httptest.NewServer(http.NotFoundHandler())
	kokoro.Close() // nothing listens at its address now
	dir := t.TempDir()
	file := "---\nid: 01a1de8b-bba7-7000-bad0-0000000000a1\nname: Lighthouse keeper\nvoice: bf_emma\n---\nYou answer every call from the sea.\n"
	if err := os.WriteFile(filepath.Join(dir, "lighthouse-keeper.md"), []byte(file), 0o600); err != nil {
		t.Fatalf("write the agent file: %v", err)
	}
	getenv := env(map[string]string{"DATABASE_URL": "postgres://keeper@127.0.0.1:1/harbour?sslmode=disable", "VOICE_AGENTS_DIR": dir, "VOICE_TTS_URL": kokoro.URL})
	var out strings.Builder
	_ = run(context.Background(), getenv, slog.New(slog.NewTextHandler(&out, nil)), publishTimeout)
	if !strings.Contains(out.String(), "not checked") {
		t.Errorf("log %q, want a warning that the voices were not checked", out.String())
	}
}

func TestPublishAsksKokoroWhereComposePublishesItUnlessTheTextToSpeechIsTheFake(t *testing.T) {
	if cfg, _ := loadConfig(env(nil)); cfg.ttsURL != defaultTTSURL || voiceList(cfg) == nil {
		t.Errorf("ttsURL = %q, want %q where compose publishes Kokoro, with a list to ask", cfg.ttsURL, defaultTTSURL)
	}
	if cfg, _ := loadConfig(env(map[string]string{"VOICE_TTS": "fake"})); cfg.ttsURL != "" || voiceList(cfg) != nil {
		t.Errorf("ttsURL = %q with the fake text-to-speech, want none to ask", cfg.ttsURL)
	}
}

// Through run, with the agent file and the service's answer as a person's publish has them, and a
// database no one listens on: the refusal comes before it is reached.
func TestPublishRefusesAnAgentFileNamingAVoiceTheServiceLacks(t *testing.T) {
	kokoro := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"voices":["af_heart"]}`))
	}))
	defer kokoro.Close()
	dir := t.TempDir()
	file := "---\nid: 01a1de8b-bba7-7000-bad0-0000000000a1\nname: Lighthouse keeper\nvoice: bf_emma\n---\nYou answer every call from the sea.\n"
	if err := os.WriteFile(filepath.Join(dir, "lighthouse-keeper.md"), []byte(file), 0o600); err != nil {
		t.Fatalf("write the agent file: %v", err)
	}
	getenv := env(map[string]string{"DATABASE_URL": "postgres://keeper@127.0.0.1:1/harbour?sslmode=disable", "VOICE_AGENTS_DIR": dir, "VOICE_TTS_URL": kokoro.URL})
	err := run(context.Background(), getenv, slog.New(slog.DiscardHandler), publishTimeout)
	if err == nil || !strings.Contains(err.Error(), "Lighthouse keeper") || !strings.Contains(err.Error(), "bf_emma") {
		t.Errorf("run = %v, want a refusal naming the agent and its voice", err)
	}
}
