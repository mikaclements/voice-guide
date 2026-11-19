// Command publish saves each agent file in VOICE_AGENTS_DIR as its agent's
// next version, for the workspace VOICE_WORKSPACE_ID, when its settings or
// persona changed. The workspace runs it whenever it changes an agent, through
// scripts/publish.sh; it is never part of a service's start, so a service
// still on an older build can't publish its older text.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/agents/agentfile"
	"github.com/mikaclements/voice-guide/internal/agents/store/postgres"
	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/speech/tts/kokoro"
	"github.com/mikaclements/voice-guide/internal/usercontext"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(context.Background(), os.Getenv, log, publishTimeout); err != nil {
		log.Error("publish failed", "err", err)
		os.Exit(1)
	}
}

// publishTimeout bounds publishing, so a database that never answers fails it
// rather than hanging scripts/run.sh. Publishing is a few queries per agent,
// well under a second, so a minute is a wide margin: a round guess.
const publishTimeout = time.Minute

type config struct {
	databaseURL string
	workspace   uuid.UUID
	agentsDir   string
	// ttsURL is where Kokoro-FastAPI, or the MLX server, lists its voices; empty with the fake
	// text-to-speech, which has no voices to check against.
	ttsURL string
}

// defaultTTSURL is where compose publishes Kokoro-FastAPI on this machine, as cmd/voice's.
const defaultTTSURL = "http://127.0.0.1:8880"

// voicesTimeout bounds asking the text-to-speech service for its voices, which is a few hundred
// bytes from a service on this network, so 2s is a wide margin without holding up a publish when it
// isn't running. A round guess, to be measured the first time a publish warns that Kokoro was
// slow rather than down.
const voicesTimeout = 2 * time.Second

func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{databaseURL: getenv("DATABASE_URL"), agentsDir: getenv("VOICE_AGENTS_DIR")}
	if cfg.databaseURL == "" {
		return config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.agentsDir == "" {
		cfg.agentsDir = "agents"
	}
	var err error
	if cfg.workspace, err = uuid.Parse(getenv("VOICE_WORKSPACE_ID")); err != nil {
		return config{}, fmt.Errorf("VOICE_WORKSPACE_ID: %w", err)
	}
	if getenv("VOICE_TTS") != "fake" {
		cfg.ttsURL = cmp.Or(getenv("VOICE_TTS_URL"), defaultTTSURL)
	}
	return cfg, nil
}

func run(ctx context.Context, getenv func(string) string, log *slog.Logger, timeout time.Duration) error {
	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defs, err := agentfile.Load(ctx, os.DirFS(cfg.agentsDir))
	if err != nil {
		return fmt.Errorf("load agents from %s: %w", cfg.agentsDir, err)
	}
	if err := agents.CheckReads(defs, usercontext.Kinds()); err != nil {
		return err
	}
	if err := checkVoices(ctx, defs, voiceList(cfg), log); err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	published, err := agents.Publish(ctx, postgres.New(pool), cfg.workspace, defs)
	for _, p := range published {
		msg := "unchanged"
		if p.New {
			msg = "published"
		}
		log.Info(msg, "agent", p.Name, "id", p.Agent, "version", p.Number)
	}
	return err
}

// voiceList is the text-to-speech service's list of voices, or nil when there is none to ask.
func voiceList(cfg config) conversation.VoiceList {
	if cfg.ttsURL == "" {
		return nil
	}
	return kokoro.New(cfg.ttsURL, kokoro.Options{})
}

// checkVoices refuses an agent naming a voice the text-to-speech service doesn't have. It asks
// only when an agent names a voice, and when the service can't be reached it publishes anyway and
// says the voices weren't checked: the service needn't be running to publish agents.
func checkVoices(ctx context.Context, defs []agents.Agent, voices conversation.VoiceList, log *slog.Logger) error {
	if voices == nil || !slices.ContainsFunc(defs, func(a agents.Agent) bool { return a.Voice != "" }) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, voicesTimeout)
	defer cancel()
	offered, err := voices.Voices(ctx)
	if err != nil {
		log.Warn("the text-to-speech service couldn't be asked for its voices, so the agents' voices are not checked", "err", err)
		return nil
	}
	return agents.CheckVoices(defs, offered)
}
