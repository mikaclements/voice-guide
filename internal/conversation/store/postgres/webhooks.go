package postgres

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/conversation/store/postgres/db"
)

// Webhooks keeps each workspace's webhook in PostgreSQL, the secret readable,
// since signing needs it.
type Webhooks struct {
	q *db.Queries
}

// NewWebhooks returns the store on pool.
func NewWebhooks(pool *pgxpool.Pool) *Webhooks {
	return &Webhooks{q: db.New(pool)}
}

// Webhook returns the workspace's webhook, if it has one.
func (st *Webhooks) Webhook(ctx context.Context, workspace uuid.UUID) (conversation.Webhook, bool, error) {
	r, err := st.q.Webhook(ctx, workspace)
	if errors.Is(err, pgx.ErrNoRows) {
		return conversation.Webhook{}, false, nil
	}
	if err != nil {
		return conversation.Webhook{}, false, fmt.Errorf("read the webhook: %w", err)
	}
	return conversation.Webhook{URL: r.Url, Secret: conversation.NewWebhookSecret(r.Secret), Stopped: r.Stopped}, true, nil
}

// SetURL saves url as the workspace's webhook URL, starting deliveries again,
// with secret unless it has one already, and returns the secret kept.
func (st *Webhooks) SetURL(ctx context.Context, workspace uuid.UUID, url string, secret conversation.WebhookSecret) (conversation.WebhookSecret, error) {
	kept, err := st.q.SetWebhookURL(ctx, db.SetWebhookURLParams{WorkspaceID: workspace, Url: url, Secret: secret.Reveal()})
	if err != nil {
		return conversation.WebhookSecret{}, fmt.Errorf("set the webhook's URL: %w", err)
	}
	return conversation.NewWebhookSecret(kept), nil
}

// Rotate replaces the workspace's webhook secret, and says whether it has a webhook.
func (st *Webhooks) Rotate(ctx context.Context, workspace uuid.UUID, secret conversation.WebhookSecret) (bool, error) {
	n, err := st.q.RotateWebhookSecret(ctx, db.RotateWebhookSecretParams{WorkspaceID: workspace, Secret: secret.Reveal()})
	if err != nil {
		return false, fmt.Errorf("rotate the webhook's secret: %w", err)
	}
	return n == 1, nil
}

// Stop stops deliveries to the workspace's webhook while its URL is still url.
func (st *Webhooks) Stop(ctx context.Context, workspace uuid.UUID, url string) error {
	if err := st.q.StopWebhook(ctx, db.StopWebhookParams{WorkspaceID: workspace, Url: url}); err != nil {
		return fmt.Errorf("stop the webhook: %w", err)
	}
	return nil
}
