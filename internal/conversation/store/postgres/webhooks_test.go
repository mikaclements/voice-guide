package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/conversation/store/postgres"
)

// Webhook secrets in the form voice-guide makes, invented for these tests.
var (
	secret  = conversation.NewWebhookSecret("whsec_bGlnaHRob3VzZS1rZWVwZXItc2VjcmV0LTMyLWJ5dGU=")
	another = conversation.NewWebhookSecret("whsec_ZmVycnktY2FwdGFpbi1zZWNyZXQtb2YtMzItYnl0ZXM=")
)

const webhookURL = "https://jobs.example/webhooks/voice-guide"

func TestAWebhooksURLMovesKeepingItsSecretAndStartsDeliveriesAgain(t *testing.T) {
	_, pool := storeWithTwoWorkspaces(t)
	webhooks, ctx := postgres.NewWebhooks(pool), context.Background()
	if kept, err := webhooks.SetURL(ctx, workspace, webhookURL, secret); err != nil || kept.Reveal() != secret.Reveal() {
		t.Fatalf("SetURL = %v; want the secret given kept by a workspace with none", err)
	}
	if err := webhooks.Stop(ctx, workspace, webhookURL); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if h, _, _ := webhooks.Webhook(ctx, workspace); !h.Stopped {
		t.Fatal("deliveries weren't stopped")
	}
	if kept, err := webhooks.SetURL(ctx, workspace, "https://jobs.example/hooks/v2", another); err != nil || kept.Reveal() != secret.Reveal() {
		t.Errorf("SetURL again = %v; want the first secret kept, not the one given", err)
	}
	if h, found, err := webhooks.Webhook(ctx, workspace); err != nil || !found || h.URL != "https://jobs.example/hooks/v2" || h.Secret.Reveal() != secret.Reveal() || h.Stopped {
		t.Errorf("Webhook = %+v, %v, %v; want the URL moved, the secret kept and deliveries started again", h, found, err)
	}
}

func TestDeliveriesStopOnlyWhileTheURLIsTheOneThatAnswered(t *testing.T) {
	_, pool := storeWithTwoWorkspaces(t)
	webhooks, ctx := postgres.NewWebhooks(pool), context.Background()
	if _, err := webhooks.SetURL(ctx, workspace, "https://jobs.example/hooks/v2", secret); err != nil {
		t.Fatalf("SetURL: %v", err)
	}
	if err := webhooks.Stop(ctx, workspace, webhookURL); err != nil { // the old URL's server answered 410
		t.Fatalf("Stop: %v", err)
	}
	if h, _, _ := webhooks.Webhook(ctx, workspace); h.Stopped {
		t.Error("the URL set since was stopped for the old one's answer")
	}
}

func TestRotatingReplacesOnlyTheWorkspacesOwnSecret(t *testing.T) {
	_, pool := storeWithTwoWorkspaces(t)
	webhooks, ctx := postgres.NewWebhooks(pool), context.Background()
	if _, err := webhooks.SetURL(ctx, workspace, webhookURL, secret); err != nil {
		t.Fatalf("SetURL: %v", err)
	}
	if found, err := webhooks.Rotate(ctx, elsewhere, another); found || err != nil {
		t.Errorf("Rotate in a workspace with no webhook = %v, %v; want none found", found, err)
	}
	if _, found, _ := webhooks.Webhook(ctx, elsewhere); found {
		t.Error("the other workspace reads a webhook")
	}
	if found, err := webhooks.Rotate(ctx, workspace, another); !found || err != nil {
		t.Errorf("Rotate = %v, %v; want the webhook found", found, err)
	}
	if h, _, _ := webhooks.Webhook(ctx, workspace); h.Secret.Reveal() != another.Reveal() {
		t.Error("the secret wasn't replaced")
	}
}

func TestTheTableRefusesAWebhookThatIsntAnHTTPAddressOrASecretVoiceGuideMakes(t *testing.T) {
	_, pool := storeWithTwoWorkspaces(t)
	for name, row := range map[string][2]string{
		"another scheme":    {"ftp://jobs.example/webhooks", secret.Reveal()},
		"a secret too long": {webhookURL, secret.Reveal() + "AAAA"},
		"no prefix":         {webhookURL, "bGlnaHRob3VzZS1rZWVwZXItc2VjcmV0LTMyLWJ5dGU="},
	} {
		if _, err := pool.Exec(context.Background(), "INSERT INTO webhooks (workspace_id, url, secret) VALUES ($1, $2, $3)", workspace, row[0], row[1]); err == nil {
			t.Errorf("%s: saved", name)
		}
	}
}

// deliveryJob is a delivery job as the end queued it.
type deliveryJob struct {
	workspace, user, after uuid.UUID
	args                   conversation.DeliveryArgs
}

// deliveryJobs returns the delivery jobs queued, each with its args read strictly, as the job
// runner reads them, checking each waits on its session's summary job.
func deliveryJobs(t *testing.T, pool *pgxpool.Pool) []deliveryJob {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT d.workspace_id, d.user_id, d.after_id, d.args, s.kind, s.args->>'session_id' = d.args->>'session_id'
		FROM jobs d LEFT JOIN jobs s ON s.id = d.after_id
		WHERE d.kind = $1`, conversation.DeliveryJob)
	if err != nil {
		t.Fatalf("read the delivery jobs: %v", err)
	}
	defer rows.Close()
	var out []deliveryJob
	for rows.Next() {
		var j deliveryJob
		var raw []byte
		var afterKind *string
		var sameSession *bool
		if err := rows.Scan(&j.workspace, &j.user, &j.after, &raw, &afterKind, &sameSession); err != nil {
			t.Fatalf("scan: %v", err)
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&j.args); err != nil || j.args.Delivery == uuid.Nil() {
			t.Errorf("args %s = %v; want a session and a delivery ID, read strictly", raw, err)
		}
		if afterKind == nil || *afterKind != conversation.SummaryJob || sameSession == nil || !*sameSession {
			t.Errorf("delivery job %+v waits on a %v job, want its own session's summary job", j, afterKind)
		}
		out = append(out, j)
	}
	return out
}

func TestEndingASessionQueuesOneDeliveryAfterItsSummaryOnlyForAWorkspaceWithAWebhookNotStopped(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	webhooks, ctx := postgres.NewWebhooks(pool), context.Background()
	if _, err := webhooks.SetURL(ctx, workspace, webhookURL, secret); err != nil {
		t.Fatalf("SetURL: %v", err)
	}
	here, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	there, _ := start(t, sessions, sessionOn(elsewhere, user), uuid.NewV7()) // a workspace with no webhook
	var ends sync.WaitGroup
	for range 4 {
		for _, s := range []conversation.Session{here, there} {
			ends.Go(func() {
				if _, _, err := sessions.End(ctx, s.Workspace, s.User, s.ID); err != nil {
					t.Errorf("End: %v", err)
				}
			})
		}
	}
	ends.Wait()
	if got := deliveryJobs(t, pool); len(got) != 1 || got[0].workspace != workspace || got[0].user != user || got[0].args.Session != here.ID {
		t.Errorf("delivery jobs %+v, want one, for the session in the workspace with a webhook", got)
	}

	if err := webhooks.Stop(ctx, workspace, webhookURL); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	stopped, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7())
	if _, _, err := sessions.End(ctx, workspace, user, stopped.ID); err != nil {
		t.Fatalf("End: %v", err)
	}
	if got := deliveryJobs(t, pool); len(got) != 1 {
		t.Errorf("%d delivery jobs, want no more once deliveries were stopped", len(got))
	}
}

func TestAnIdleEndQueuesEachSessionsDeliveryOnceForItsOwnWorkspacesWebhook(t *testing.T) {
	sessions, pool := storeWithTwoWorkspaces(t)
	if _, err := postgres.NewWebhooks(pool).SetURL(context.Background(), elsewhere, webhookURL, secret); err != nil {
		t.Fatalf("SetURL: %v", err)
	}
	quiet, _ := start(t, sessions, sessionOn(elsewhere, user), uuid.NewV7())
	other, _ := start(t, sessions, sessionOn(workspace, user), uuid.NewV7()) // a workspace with no webhook
	for _, s := range []conversation.Session{quiet, other} {
		idleFor(t, pool, s.ID, time.Hour)
	}
	var sweeps sync.WaitGroup
	for range 2 { // two instances sweeping at once
		sweeps.Go(func() {
			if _, err := postgres.NewSessions(pool).EndIdle(context.Background(), idle); err != nil {
				t.Errorf("EndIdle: %v", err)
			}
		})
	}
	sweeps.Wait()
	if got := deliveryJobs(t, pool); len(got) != 1 || got[0].workspace != elsewhere || got[0].args.Session != quiet.ID {
		t.Errorf("delivery jobs %+v, want one, for the idle session in the workspace with a webhook", got)
	}
}

func TestAUsersReferenceReadsBackOnlyInItsWorkspace(t *testing.T) {
	sessions, _ := storeWithTwoWorkspaces(t)
	ctx := context.Background()
	s, _, err := sessions.Start(ctx, sessionOn(workspace, uuid.NewV7()), uuid.NewV7(), "aiko-42")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ref, found, err := sessions.Reference(ctx, workspace, s.User); err != nil || !found || ref != "aiko-42" {
		t.Errorf("Reference = %q, %v, %v; want the product's reference", ref, found, err)
	}
	for name, by := range map[string][2]uuid.UUID{"another workspace": {elsewhere, s.User}, "a user with none": {workspace, user}} {
		if ref, found, err := sessions.Reference(ctx, by[0], by[1]); found || err != nil {
			t.Errorf("%s: Reference = %q, %v, %v; want none", name, ref, found, err)
		}
	}
}

var _ conversation.Webhooks = (*postgres.Webhooks)(nil)
