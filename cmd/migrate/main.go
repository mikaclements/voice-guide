// Command migrate brings the database at DATABASE_URL up to the newest
// migration. It runs once per deploy, before new instances take traffic, and
// scripts/run.sh runs it locally; the service itself never migrates.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/mikaclements/voice-guide/migrations"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	// A deploy step that hangs blocks the deploy; this one gives up and says so. Today's migrations
	// run in well under a second, so 5 minutes leaves room for a large table later: a round guess.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := run(ctx, os.Getenv, log); err != nil {
		log.Error("migrate failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, log *slog.Logger) error {
	url := getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	applied, err := migrations.Up(ctx, url)
	for _, r := range applied {
		log.Info("migrated", "file", r.Source.Path, "ms", r.Duration.Milliseconds())
	}
	if err != nil {
		return err
	}
	log.Info("database is current", "applied", len(applied))
	return nil
}
