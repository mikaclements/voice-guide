package conversation_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/conversation"
	sessionsfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	webhookfake "github.com/mikaclements/voice-guide/internal/conversation/webhook/fake"
	"github.com/mikaclements/voice-guide/internal/redacttest"
	"github.com/mikaclements/voice-guide/internal/usercontext"
)

const webhookURL = "https://jobs.example/webhooks/voice-guide"

// attemptAt is when every attempt in these tests is made, by the clock the deliveries are given.
var attemptAt = time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC)

// delivering is the service over fakes with a webhook set for the workspace, deliveries through
// poster, and a session the product's server started for its user "aiko-42", in which the user
// took a turn, then ended.
type delivering struct {
	serviceOverFakes
	webhooks   *sessionsfake.Webhooks
	poster     *webhookfake.Poster
	deliveries *conversation.Deliveries
	secret     conversation.WebhookSecret
	session    conversation.Session
	user       access.Caller
}

func newDelivering(t *testing.T, answers ...webhookfake.Answer) delivering {
	t.Helper()
	d := delivering{serviceOverFakes: newServiceOverFakes(t), webhooks: sessionsfake.NewWebhooks(), poster: webhookfake.New(answers...)}
	d.deliveries = conversation.NewDeliveries(d.sessions, d.turns, d.webhooks, usercontext.NewService(d.items), d.poster)
	conversation.SetDeliveriesClock(d.deliveries, func() time.Time { return attemptAt })
	secret, made, err := conversation.SetWebhook(context.Background(), d.webhooks, workspace, webhookURL)
	if err != nil || !made {
		t.Fatalf("SetWebhook = %v, %v; want a secret made", made, err)
	}
	d.secret = secret
	d.session, _ = d.startFor(t, "aiko-42", uuid.NewV7())
	d.user = access.Caller{Workspace: workspace, User: d.session.User}
	if _, err := d.svc.TakeTurn(context.Background(), d.user, turnIn(d.session.ID, "Is the lamp lit?"), func(string) {}); err != nil {
		t.Fatalf("TakeTurn: %v", err)
	}
	if d.session, err = d.svc.EndSession(context.Background(), d.user, d.session.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	return d
}

func (d delivering) deliver(args conversation.DeliveryArgs) error {
	return d.deliveries.Deliver(context.Background(), workspace, d.session.User, args)
}

// verifies checks a delivery as a product's server does: the secret's bytes after whsec_ key an
// HMAC-SHA256 of the ID, the timestamp and the body joined by full stops, which the signature
// header carries after v1, in base64.
func verifies(secret conversation.WebhookSecret, posted conversation.Delivery) bool {
	key, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret.Reveal(), "whsec_"))
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(posted.Headers["webhook-id"] + "." + posted.Headers["webhook-timestamp"] + "." + string(posted.Body)))
	want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(posted.Headers["webhook-signature"]), []byte(want))
}

// body is a delivery's body as a product's server reads it.
type body struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Data      struct {
		ID           string    `json:"id"`
		User         string    `json:"user"`
		AgentID      string    `json:"agent_id"`
		AgentVersion int       `json:"agent_version"`
		StartedAt    time.Time `json:"started_at"`
		EndedAt      time.Time `json:"ended_at"`
		Turns        []struct {
			Seq   int    `json:"seq"`
			Words string `json:"words"`
			Reply string `json:"reply"`
			Cut   bool   `json:"cut"`
		} `json:"turns"`
		TurnsLeftOut  int     `json:"turns_left_out"`
		Summary       *string `json:"summary"`
		SummaryStatus string  `json:"summary_status"`
	} `json:"data"`
}

func read(t *testing.T, d conversation.Delivery) body {
	t.Helper()
	var b body
	dec := json.NewDecoder(bytes.NewReader(d.Body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		t.Fatalf("the body %s doesn't read: %v", d.Body, err)
	}
	return b
}

func TestADeliveryIsSignedSoItsProductCanCheckItAndWhenItWasSent(t *testing.T) {
	d := newDelivering(t)
	args := conversation.DeliveryArgs{Session: d.session.ID, Delivery: uuid.NewV7()}
	if err := d.deliver(args); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	posted := d.poster.Posted()
	if len(posted) != 1 || posted[0].URL != webhookURL {
		t.Fatalf("posted %+v, want one delivery, to the webhook's URL", posted)
	}
	h := posted[0].Headers
	switch {
	case len(h) != 3 || h["webhook-id"] != args.Delivery.String():
		t.Errorf("headers %v, want the three, webhook-id the delivery's ID %s", h, args.Delivery)
	case h["webhook-timestamp"] != strconv.FormatInt(attemptAt.Unix(), 10):
		t.Errorf("webhook-timestamp %q, want the attempt's time in seconds, %d", h["webhook-timestamp"], attemptAt.Unix())
	case !strings.HasPrefix(h["webhook-signature"], "v1,") || !verifies(d.secret, posted[0]):
		t.Errorf("webhook-signature %q doesn't verify with the webhook's secret", h["webhook-signature"])
	}
}

func TestADeliveryCarriesTheSessionItsTurnsAsHeardAndItsSummary(t *testing.T) {
	d := newDelivering(t)
	if _, err := usercontext.NewService(d.items).SaveSummary(context.Background(), usercontext.SummaryRequest{
		Workspace: workspace, User: d.session.User, Agent: keeperID, Session: d.session.ID, SessionEnded: d.session.EndedAt, Content: "Covered the lamp.",
	}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	args := conversation.DeliveryArgs{Session: d.session.ID, Delivery: uuid.NewV7()}
	if err := d.deliver(args); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	posted := d.poster.Posted()
	if len(posted) != 1 {
		t.Fatalf("posted %d deliveries, want one", len(posted))
	}
	b, s := read(t, posted[0]), d.session
	switch {
	case b.ID != args.Delivery.String() || b.Type != "session.ended" || !b.Timestamp.Equal(s.EndedAt):
		t.Errorf("delivery %s, %q at %s; want its ID, session.ended, when the session ended", b.ID, b.Type, b.Timestamp)
	case b.Data.ID != s.ID.String() || b.Data.User != "aiko-42" || b.Data.AgentID != keeperID.String() || b.Data.AgentVersion != s.AgentVersion:
		t.Errorf("session %+v, want its ID, the product's reference for the user, never voice-guide's user ID, its agent and version", b.Data)
	case !b.Data.StartedAt.Equal(s.StartedAt) || !b.Data.EndedAt.Equal(s.EndedAt):
		t.Errorf("started %s and ended %s, want %s and %s", b.Data.StartedAt, b.Data.EndedAt, s.StartedAt, s.EndedAt)
	case len(b.Data.Turns) != 1 || b.Data.Turns[0].Seq != 1 || b.Data.Turns[0].Words != "Is the lamp lit?" || b.Data.Turns[0].Reply != reply || b.Data.Turns[0].Cut:
		t.Errorf("turns %+v, want the one turn, its words and whole reply, not cut", b.Data.Turns)
	case b.Data.TurnsLeftOut != 0 || b.Data.Summary == nil || *b.Data.Summary != "Covered the lamp.":
		t.Errorf("left out %d, summary %v; want none left out and the summary", b.Data.TurnsLeftOut, b.Data.Summary)
	case strings.Contains(string(posted[0].Body), d.session.User.String()):
		t.Error("the body holds voice-guide's user ID")
	}
}

func TestASessionWithNoTurnIsDeliveredWithNoTurnsOrSummary(t *testing.T) {
	d := newDelivering(t)
	s, _ := d.start(t, caller, keeperID, uuid.NewV7())
	if _, err := d.svc.EndSession(context.Background(), caller, s.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if err := d.deliveries.Deliver(context.Background(), workspace, user, conversation.DeliveryArgs{Session: s.ID, Delivery: uuid.NewV7()}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	posted := d.poster.Posted()
	if len(posted) != 1 {
		t.Fatalf("posted %d deliveries, want one", len(posted))
	}
	if b := read(t, posted[0]); b.Data.Turns == nil || len(b.Data.Turns) != 0 || b.Data.Summary != nil {
		t.Errorf("session %+v, want an empty list of turns and a null summary", b.Data)
	}
	if !strings.Contains(string(posted[0].Body), `"turns":[]`) || !strings.Contains(string(posted[0].Body), `"summary":null`) {
		t.Errorf("body %s, want turns an empty list and summary null, not left out", posted[0].Body)
	}
}

func TestASessionWhoseUserHasNoReferenceIsNeverDelivered(t *testing.T) {
	d := newDelivering(t)
	ctx := context.Background()
	s, _, _ := d.sessions.Start(ctx, conversation.Session{ID: uuid.NewV7(), Workspace: workspace, User: user, Agent: keeperID, AgentVersion: 1}, uuid.NewV7(), "")
	if _, _, err := d.sessions.End(ctx, workspace, user, s.ID); err != nil {
		t.Fatalf("End: %v", err)
	}
	if err := d.deliveries.Deliver(ctx, workspace, user, conversation.DeliveryArgs{Session: s.ID, Delivery: uuid.NewV7()}); err == nil || len(d.poster.Posted()) != 0 {
		t.Errorf("Deliver = %v with %d posted; want an error and nothing posted without the user", err, len(d.poster.Posted()))
	}
}

func TestAnOpeningIsDeliveredAsTheFirstTurnWithTheGreetingAndNoWords(t *testing.T) {
	d := newDelivering(t)
	d.publish(t, agents.Agent{ID: captainID, Name: "Ferry captain", Persona: "You know every crossing by heart.", Opens: true})
	s, _ := d.start(t, caller, captainID, uuid.NewV7())
	d.takeTurn(t, openingIn(s.ID))
	if _, err := d.svc.EndSession(context.Background(), caller, s.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if err := d.deliveries.Deliver(context.Background(), workspace, user, conversation.DeliveryArgs{Session: s.ID, Delivery: uuid.NewV7()}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	posted := d.poster.Posted()
	if len(posted) != 1 {
		t.Fatalf("posted %d deliveries, want one", len(posted))
	}
	if turns := read(t, posted[0]).Data.Turns; len(turns) != 1 || turns[0].Seq != 1 || turns[0].Words != "" || turns[0].Reply != reply {
		t.Errorf("turns %+v, want the opening as turn 1: no words, the greeting", turns)
	}
}

func TestATurnCutAfterTheEndIsDeliveredAsFarAsItWasHeard(t *testing.T) {
	d := newDelivering(t)
	// The page hung up mid-reply; its cut was saved after the end, before the delivery ran.
	if _, err := d.svc.Interrupted(context.Background(), d.turns.All()[0], len("  **Bearing")); err != nil {
		t.Fatalf("Interrupted: %v", err)
	}
	if err := d.deliver(conversation.DeliveryArgs{Session: d.session.ID, Delivery: uuid.NewV7()}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if turns := read(t, d.poster.Posted()[0]).Data.Turns; len(turns) != 1 || turns[0].Reply != "  **Bearing" || !turns[0].Cut {
		t.Errorf("turns %+v, want the reply as far as it was heard, cut", turns)
	}
}

func TestEveryAttemptPostsTheSameIDToTheURLAndWithTheSecretTheWebhookHasThen(t *testing.T) {
	d := newDelivering(t, webhookfake.Answer{Status: http.StatusServiceUnavailable})
	args := conversation.DeliveryArgs{Session: d.session.ID, Delivery: uuid.NewV7()}
	if err := d.deliver(args); err == nil || errors.Is(err, conversation.ErrWebhookStopped) {
		t.Fatalf("Deliver on a 503 = %v, want an error to try again", err)
	}
	const moved = "https://jobs.example/hooks/v2"
	if _, made, err := conversation.SetWebhook(context.Background(), d.webhooks, workspace, moved); err != nil || made {
		t.Fatalf("SetWebhook = %v, %v; want the URL moved, the secret kept", made, err)
	}
	rotated, err := conversation.RotateWebhookSecret(context.Background(), d.webhooks, workspace)
	if err != nil {
		t.Fatalf("RotateWebhookSecret: %v", err)
	}
	if err := d.deliver(args); err != nil {
		t.Fatalf("Deliver again: %v", err)
	}
	posted := d.poster.Posted()
	if len(posted) != 2 || posted[1].Headers["webhook-id"] != posted[0].Headers["webhook-id"] || posted[1].URL != moved {
		t.Fatalf("posted %+v; want the second attempt with the same ID, to the URL set since", posted)
	}
	if !verifies(d.secret, posted[0]) || !verifies(rotated, posted[1]) || verifies(d.secret, posted[1]) {
		t.Error("want the first attempt signed with the first secret, the second with the secret rotated since, and not the first")
	}
}

func TestEveryAnswerBut2xxIsTriedAgainSave410WhichStopsTheWebhook(t *testing.T) {
	const done, again, stopped = "done", "tried again", "stopped"
	tests := map[string]struct {
		answer webhookfake.Answer
		want   string
	}{
		"200":                    {webhookfake.Answer{Status: 200}, done},
		"204":                    {webhookfake.Answer{Status: 204}, done},
		"302, not followed":      {webhookfake.Answer{Status: 302}, again},
		"400":                    {webhookfake.Answer{Status: 400}, again},
		"401, a signature check": {webhookfake.Answer{Status: 401}, again},
		"403":                    {webhookfake.Answer{Status: 403}, again},
		"404, mid-deploy":        {webhookfake.Answer{Status: 404}, again},
		"408 Request Timeout":    {webhookfake.Answer{Status: 408}, again},
		"413 Content Too Large":  {webhookfake.Answer{Status: 413}, again},
		"429 Too Many Requests":  {webhookfake.Answer{Status: 429}, again},
		"500":                    {webhookfake.Answer{Status: 500}, again},
		"503":                    {webhookfake.Answer{Status: 503}, again},
		"no answer":              {webhookfake.Answer{Err: errors.New("the product's server answered nothing within the deadline")}, again},
		"410 Gone":               {webhookfake.Answer{Status: 410}, stopped},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			d := newDelivering(t, tt.answer)
			err := d.deliver(conversation.DeliveryArgs{Session: d.session.ID, Delivery: uuid.NewV7()})
			got := done
			switch {
			case errors.Is(err, conversation.ErrWebhookStopped):
				got = stopped
			case err != nil:
				got = again
			}
			if got != tt.want {
				t.Errorf("Deliver = %v: %s, want %s", err, got, tt.want)
			}
		})
	}
}

func TestA410StopsDeliveriesUntilTheURLIsSetAgainNamingEachSessionNotDelivered(t *testing.T) {
	d := newDelivering(t, webhookfake.Answer{Status: http.StatusGone})
	args := conversation.DeliveryArgs{Session: d.session.ID, Delivery: uuid.NewV7()}
	if err := d.deliver(args); !errors.Is(err, conversation.ErrWebhookStopped) {
		t.Fatalf("Deliver on a 410 = %v, want ErrWebhookStopped", err)
	}
	err := d.deliver(conversation.DeliveryArgs{Session: d.session.ID, Delivery: uuid.NewV7()})
	if !errors.Is(err, conversation.ErrWebhookStopped) || !strings.Contains(err.Error(), d.session.ID.String()) || strings.Contains(err.Error(), webhookURL) || len(d.poster.Posted()) != 1 {
		t.Fatalf("Deliver once stopped = %v, with %d posts; want nothing posted, and ErrWebhookStopped naming the session, never the URL", err, len(d.poster.Posted()))
	}
	if _, _, err := conversation.SetWebhook(context.Background(), d.webhooks, workspace, webhookURL); err != nil {
		t.Fatalf("SetWebhook: %v", err)
	}
	if err := d.deliver(args); err != nil || len(d.poster.Posted()) != 2 {
		t.Errorf("Deliver once the URL was set again = %v, with %d posts; want it posted", err, len(d.poster.Posted()))
	}
}

func TestAURLKeptBeforeTheRuleIsNeverPostedTo(t *testing.T) {
	d := newDelivering(t)
	const plain = "http://jobs.example/webhooks" // kept straight in the store, as before the rule
	if _, err := d.webhooks.SetURL(context.Background(), workspace, plain, d.secret); err != nil {
		t.Fatalf("SetURL: %v", err)
	}
	err := d.deliver(conversation.DeliveryArgs{Session: d.session.ID, Delivery: uuid.NewV7()})
	if !errors.Is(err, conversation.ErrWebhookURL) || !strings.Contains(err.Error(), d.session.ID.String()) || strings.Contains(err.Error(), plain) || len(d.poster.Posted()) != 0 {
		t.Errorf("Deliver = %v, with %d posts; want nothing posted, and ErrWebhookURL naming the session, never the URL", err, len(d.poster.Posted()))
	}
}

func TestAWorkspaceWithNoWebhookIsPostedNothing(t *testing.T) {
	d := newDelivering(t)
	none := conversation.NewDeliveries(d.sessions, d.turns, sessionsfake.NewWebhooks(), usercontext.NewService(d.items), d.poster)
	if err := none.Deliver(context.Background(), workspace, d.session.User, conversation.DeliveryArgs{Session: d.session.ID, Delivery: uuid.NewV7()}); err != nil || len(d.poster.Posted()) != 0 {
		t.Errorf("Deliver = %v, with %d posts; want nothing posted", err, len(d.poster.Posted()))
	}
}

func TestSettingAWebhookMakesItsSecretOnceAndKeepsItWhenTheURLMoves(t *testing.T) {
	webhooks, ctx := sessionsfake.NewWebhooks(), context.Background()
	first, made, err := conversation.SetWebhook(ctx, webhooks, workspace, webhookURL)
	if err != nil || !made || !strings.HasPrefix(first.Reveal(), "whsec_") || len(first.Reveal()) != len("whsec_")+44 {
		t.Fatalf("SetWebhook = %v, %v; want a secret made: whsec_ and 32 bytes in base64", made, err)
	}
	if again, made, err := conversation.SetWebhook(ctx, webhooks, workspace, "https://jobs.example/hooks/v2"); err != nil || made || again.Reveal() != "" {
		t.Errorf("SetWebhook again = %v, %v; want the URL moved, no secret made or shown", made, err)
	}
	if w, _, _ := webhooks.Webhook(ctx, workspace); w.Secret.Reveal() != first.Reveal() || w.URL != "https://jobs.example/hooks/v2" {
		t.Errorf("webhook %s, secret kept %v; want the new URL with the first secret", w.URL, w.Secret.Reveal() == first.Reveal())
	}
	rotated, err := conversation.RotateWebhookSecret(ctx, webhooks, workspace)
	if w, _, _ := webhooks.Webhook(ctx, workspace); err != nil || rotated.Reveal() == first.Reveal() || w.Secret.Reveal() != rotated.Reveal() {
		t.Errorf("RotateWebhookSecret = %v; want a new secret, kept", err)
	}
	if _, err := conversation.RotateWebhookSecret(ctx, webhooks, uuid.NewV7()); !errors.Is(err, conversation.ErrNoWebhook) {
		t.Errorf("RotateWebhookSecret with no webhook = %v, want ErrNoWebhook", err)
	}
}

func TestAWebhookURLMustBeHTTPSUnlessItIsOnThisMachine(t *testing.T) {
	longest := "https://jobs.example/" + strings.Repeat("a", 2000-len("https://jobs.example/"))
	for url, ok := range map[string]bool{
		webhookURL:                       true,
		"http://127.0.0.1:5190/webhook":  true,
		"http://localhost:5190/webhook":  true,
		"http://[::1]:5190/webhook":      true,
		"https://10.0.0.5/webhook":       true,
		longest:                          true,
		longest + "a":                    false,
		"http://jobs.example/webhooks":   false,
		"http://10.0.0.5/webhook":        false,
		"http://localhost.jobs.example/": false,
		"":                               false,
		"jobs.example/webhooks":          false,
		"ftp://jobs.example/webhooks":    false,
		"HTTPS://jobs.example/webhooks":  false,
		"https:///webhooks":              false,
		"https://jobs.example/%zz":       false,
	} {
		_, _, err := conversation.SetWebhook(context.Background(), sessionsfake.NewWebhooks(), workspace, url)
		if (err == nil) != ok || (err != nil && !errors.Is(err, conversation.ErrWebhookURL)) {
			t.Errorf("SetWebhook(%.40q) = %v, want taken %v", url, err, ok)
		}
	}
}

func TestAWebhookSecretShowsItselfRedacted(t *testing.T) {
	secret := conversation.NewWebhookSecret("whsec_bGlnaHRob3VzZS1rZWVwZXItc2VjcmV0LTMyLWJ5dGU=")
	redacttest.Check(t, secret, secret.Reveal(), "conversation.WebhookSecret(redacted)")
	holder := struct{ Secret conversation.WebhookSecret }{secret}
	encoded, _ := json.Marshal(holder)
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("rotated", "secret", secret)
	for _, shown := range []string{fmt.Sprint(secret), fmt.Sprintf("%s %q %v %+v %#v", secret, secret, holder, holder, holder), string(encoded), logged.String()} {
		if strings.Contains(shown, "bGln") {
			t.Errorf("the secret shows in %s", shown)
		}
	}
}

func TestADeliveryCarriesTheSummarysStatusSettledAsTheJobLeftIt(t *testing.T) {
	d := newDelivering(t)
	// The summary job failed for good, leaving the summary pending: the delivery says none.
	if err := d.deliver(conversation.DeliveryArgs{Session: d.session.ID, Delivery: uuid.NewV7()}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if b := read(t, d.poster.Posted()[0]); b.Data.SummaryStatus != "none" {
		t.Errorf("status %q, want none", b.Data.SummaryStatus)
	}
}
