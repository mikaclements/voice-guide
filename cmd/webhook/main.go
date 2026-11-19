// Command webhook sets, rotates and shows the webhook of the workspace
// VOICE_WORKSPACE_ID: where its product's server hears that its sessions
// ended. Until members manage it, use it through scripts/webhook.sh:
//
//	set URL    posts each ended session to URL, https or http on this machine, from now on,
//	           starting deliveries again if a 410 Gone stopped them; the first time, prints the
//	           secret deliveries are signed with, the only time it is shown
//	rotate     prints a new secret, the only time it is shown: every attempt from now on is
//	           signed with it, and the old one signs nothing more
//	show       the URL, and whether deliveries are stopped; never the secret
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/conversation/store/postgres"
)

// timeout bounds a command, a query or two, so a database that never answers
// fails it rather than hanging: a round guess.
const timeout = 30 * time.Second

const usage = "usage: webhook set URL | rotate | show"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "webhook:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	workspace, err := uuid.Parse(getenv("VOICE_WORKSPACE_ID"))
	if err != nil {
		return fmt.Errorf("VOICE_WORKSPACE_ID: %w", err)
	}
	if len(args) == 0 {
		return errors.New(usage)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pool, err := pgxpool.New(ctx, getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	return command(ctx, postgres.NewWebhooks(pool), workspace, args, out)
}

// command runs what args ask for on the workspace's webhook, writing to out.
func command(ctx context.Context, webhooks conversation.Webhooks, workspace uuid.UUID, args []string, out io.Writer) error {
	switch {
	case len(args) == 2 && args[0] == "set":
		secret, made, err := conversation.SetWebhook(ctx, webhooks, workspace, args[1])
		if err != nil {
			return err
		}
		if !made {
			_, err = fmt.Fprintln(out, "webhook URL set; its secret is unchanged (rotate makes a new one)")
			return err
		}
		_, err = fmt.Fprintf(out, "webhook URL set; deliveries are signed with this secret, shown only now:\n%s\n", secret.Reveal())
		return err
	case len(args) == 1 && args[0] == "rotate":
		secret, err := conversation.RotateWebhookSecret(ctx, webhooks, workspace)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "new secret, shown only now; every attempt from now on is signed with it, and the old one signs nothing more:\n%s\n", secret.Reveal())
		return err
	case len(args) == 1 && args[0] == "show":
		webhook, found, err := webhooks.Webhook(ctx, workspace)
		switch {
		case err != nil:
			return err
		case !found:
			return conversation.ErrNoWebhook
		case webhook.Stopped:
			_, err = fmt.Fprintf(out, "%s  stopped: its server answered 410 Gone; set the URL again to start deliveries again\n", webhook.URL)
			return err
		}
		_, err = fmt.Fprintln(out, webhook.URL)
		return err
	}
	return errors.New(usage)
}
