package fake

import (
	"context"
	"sync"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/conversation"
)

// Webhooks keeps each workspace's webhook in memory. It is safe for concurrent use.
type Webhooks struct {
	mu       sync.Mutex
	webhooks map[uuid.UUID]conversation.Webhook
}

// NewWebhooks returns a store with no webhooks.
func NewWebhooks() *Webhooks {
	return &Webhooks{webhooks: make(map[uuid.UUID]conversation.Webhook)}
}

// Webhook returns the workspace's webhook, if it has one.
func (f *Webhooks) Webhook(ctx context.Context, workspace uuid.UUID) (conversation.Webhook, bool, error) {
	if err := ctx.Err(); err != nil {
		return conversation.Webhook{}, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	webhook, ok := f.webhooks[workspace]
	return webhook, ok, nil
}

// SetURL saves url, starting deliveries again, with secret unless the workspace has one already.
func (f *Webhooks) SetURL(ctx context.Context, workspace uuid.UUID, url string, secret conversation.WebhookSecret) (conversation.WebhookSecret, error) {
	if err := ctx.Err(); err != nil {
		return conversation.WebhookSecret{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if webhook, ok := f.webhooks[workspace]; ok {
		secret = webhook.Secret
	}
	f.webhooks[workspace] = conversation.Webhook{URL: url, Secret: secret}
	return secret, nil
}

// Rotate replaces the workspace's secret, if it has a webhook.
func (f *Webhooks) Rotate(ctx context.Context, workspace uuid.UUID, secret conversation.WebhookSecret) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	webhook, ok := f.webhooks[workspace]
	if ok {
		webhook.Secret = secret
		f.webhooks[workspace] = webhook
	}
	return ok, nil
}

// Stop stops deliveries to the workspace's webhook while its URL is still url.
func (f *Webhooks) Stop(ctx context.Context, workspace uuid.UUID, url string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if webhook, ok := f.webhooks[workspace]; ok && webhook.URL == url {
		webhook.Stopped = true
		f.webhooks[workspace] = webhook
	}
	return nil
}
