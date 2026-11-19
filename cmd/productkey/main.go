// Command productkey makes, lists and revokes the product keys of the
// workspace VOICE_WORKSPACE_ID, until members manage them. Use it through
// scripts/product-key.sh:
//
//	create         prints a new key, the only time it is shown
//	list           each key's ID, first characters, and when it was made and revoked
//	revoke ID      refuses the key from now on, warning when no working key is left;
//	               a start already past its key check finishes, and sessions go on
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

	"github.com/mikaclements/voice-guide/internal/productkey"
	"github.com/mikaclements/voice-guide/internal/productkey/store/postgres"
)

// timeout bounds a command, a query or two, so a database that never answers
// fails it rather than hanging: a round guess.
const timeout = 30 * time.Second

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "productkey:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	workspace, err := uuid.Parse(getenv("VOICE_WORKSPACE_ID"))
	if err != nil {
		return fmt.Errorf("VOICE_WORKSPACE_ID: %w", err)
	}
	if len(args) == 0 {
		return errors.New("usage: productkey create | list | revoke ID")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pool, err := pgxpool.New(ctx, getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	return command(ctx, productkey.NewKeys(postgres.New(pool)), workspace, args, out)
}

// command runs what args ask for on the workspace's keys, writing to out.
func command(ctx context.Context, keys *productkey.Keys, workspace uuid.UUID, args []string, out io.Writer) error {
	switch {
	case len(args) == 0:
	case args[0] == "create" && len(args) == 1:
		key, _, err := keys.Create(ctx, workspace)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, key.Reveal())
		return err
	case args[0] == "list" && len(args) == 1:
		list, err := keys.List(ctx, workspace)
		if err != nil {
			return err
		}
		for _, r := range list {
			revoked := "working"
			if !r.RevokedAt.IsZero() {
				revoked = "revoked " + r.RevokedAt.UTC().Format(time.RFC3339)
			}
			if _, err := fmt.Fprintf(out, "%s  %s…  made %s  %s\n", r.ID, r.Display, r.CreatedAt.UTC().Format(time.RFC3339), revoked); err != nil {
				return err
			}
		}
		return nil
	case args[0] == "revoke" && len(args) == 2:
		id, err := uuid.Parse(args[1])
		if err != nil {
			return fmt.Errorf("key ID: %w", err)
		}
		found, working, err := keys.Revoke(ctx, workspace, id)
		switch {
		case !found && err != nil:
			return err
		case !found:
			return fmt.Errorf("the workspace has no key %s", id)
		}
		// Said even when counting the keys left failed: the revoke itself is done.
		if _, werr := fmt.Fprintf(out, "revoked %s: refused from now on; a start already past its key check finishes, within its deadline, and sessions already started go on\n", id); werr != nil {
			return werr
		}
		if err != nil {
			return fmt.Errorf("count the keys still working: %w", err)
		}
		if working == 0 {
			_, err = fmt.Fprintln(out, "warning: the workspace has no working key left, so every start from its product is refused until one is created")
		}
		return err
	}
	return errors.New("usage: productkey create | list | revoke ID")
}
