package conversation

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/usercontext"
)

// DeliveryJob is the kind of background job that posts an ended session to
// its workspace's webhook. Ending a session queues one in the same write,
// when the workspace has a webhook, to run once the session's summary job is
// done or has failed for good, so it carries the summary when there is one.
const DeliveryJob = "session_delivery"

// DeliveryArgs are a delivery job's inputs, as ending a session queues them:
// the session, and the delivery's ID, the same on every attempt.
type DeliveryArgs struct {
	Session  uuid.UUID `json:"session_id"`
	Delivery uuid.UUID `json:"delivery_id"`
}

// Webhook is where a workspace's product hears that its sessions ended: an
// address on the product's server, and the secret deliveries are signed with.
type Webhook struct {
	URL    string
	Secret WebhookSecret
	// Stopped says the product's server answered 410 Gone: nothing is
	// delivered until the URL is set again.
	Stopped bool
}

// Webhooks keeps each workspace's webhook.
type Webhooks interface {
	// Webhook returns the workspace's webhook, if it has one.
	Webhook(ctx context.Context, workspace uuid.UUID) (Webhook, bool, error)
	// SetURL saves url as the workspace's webhook URL, starting deliveries
	// again if they were stopped, with secret as its secret unless it has one
	// already, and returns the secret kept.
	SetURL(ctx context.Context, workspace uuid.UUID, url string, secret WebhookSecret) (WebhookSecret, error)
	// Rotate replaces the workspace's webhook secret with secret, and says
	// whether the workspace has a webhook.
	Rotate(ctx context.Context, workspace uuid.UUID, secret WebhookSecret) (bool, error)
	// Stop stops deliveries to the workspace's webhook while its URL is still
	// url, so a URL set meanwhile isn't stopped for the old one's answer.
	Stop(ctx context.Context, workspace uuid.UUID, url string) error
}

// Poster posts deliveries to products' servers. It is a port with one real
// implementation so the network stays out of the core's tests; what is signed
// and how is decided here, as part of what products verify.
type Poster interface {
	// Post sends d's body to its URL with its headers as given, and returns
	// the HTTP status the product's server answered, or an error when it
	// answered nothing within the attempt's deadline or couldn't be reached.
	Post(ctx context.Context, d Delivery) (int, error)
}

// Delivery is one attempt to post an ended session to its workspace's
// webhook: the body, and the headers that name the delivery, the time of the
// attempt and its signature.
type Delivery struct {
	URL     string
	Body    []byte
	Headers map[string]string
}

// signed returns the headers of an attempt, made at the time given, at the
// delivery with id: the ID, the same on every attempt, so the product's server
// can drop a repeat; the time in seconds, so it can refuse an old delivery sent
// again; and v1, then the HMAC-SHA256 (RFC 2104) of the two and the body,
// joined by full stops, keyed with the secret's bytes, in base64.
func signed(secret WebhookSecret, id string, at time.Time, body []byte) (map[string]string, error) {
	encoded, ok := strings.CutPrefix(secret.Reveal(), webhookSecretPrefix)
	key, err := base64.StdEncoding.DecodeString(encoded)
	if !ok || err != nil {
		return nil, errors.New("the webhook's secret isn't in the form voice-guide makes")
	}
	timestamp := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + timestamp + "."))
	mac.Write(body)
	return map[string]string{
		"webhook-id":        id,
		"webhook-timestamp": timestamp,
		"webhook-signature": "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)),
	}, nil
}

// webhookSecretPrefix marks a webhook secret: whsec_, then 32 random bytes in
// base64, the form products' verifying code reads as it is.
const webhookSecretPrefix = "whsec_"

// webhookSecretBytes is a secret's randomness: 256 bits, the size of the
// HMAC-SHA256 key it signs with, so it can't be guessed.
const webhookSecretBytes = 32

// redactedSecret is how a webhook secret shows itself everywhere but Reveal.
const redactedSecret = "conversation.WebhookSecret(redacted)"

// WebhookSecret is the secret a workspace's deliveries are signed with. It
// formats, logs and encodes redacted, so it never reaches a log or an error;
// Reveal gives it, to show it once or to sign with. It sits behind a pointer,
// so even printing a struct that holds one shows an address, not the secret.
type WebhookSecret struct {
	raw *string
}

// NewWebhookSecret holds raw, as kept, as a webhook secret.
func NewWebhookSecret(raw string) WebhookSecret { return WebhookSecret{raw: &raw} }

// Reveal returns the secret itself.
func (s WebhookSecret) Reveal() string {
	if s.raw == nil {
		return ""
	}
	return *s.raw
}

// String keeps the secret out of formatted output.
func (WebhookSecret) String() string { return redactedSecret }

// GoString keeps the secret out of %#v.
func (WebhookSecret) GoString() string { return redactedSecret }

// Format keeps the secret out of every formatting verb.
func (WebhookSecret) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, redactedSecret) }

// LogValue keeps the secret out of logs.
func (WebhookSecret) LogValue() slog.Value { return slog.StringValue(redactedSecret) }

// MarshalJSON keeps the secret out of anything encoded as JSON.
func (WebhookSecret) MarshalJSON() ([]byte, error) { return json.Marshal(redactedSecret) }

func makeWebhookSecret() WebhookSecret {
	random := make([]byte, webhookSecretBytes)
	_, _ = rand.Read(random) // never fails: it crashes the program instead
	return NewWebhookSecret(webhookSecretPrefix + base64.StdEncoding.EncodeToString(random))
}

// maxWebhookURL bounds a webhook URL, in characters: room for any address a
// product's server answers at, while a URL stays an address. A round guess.
const maxWebhookURL = 2000

// ErrWebhookURL reports a webhook URL that isn't an https address with a
// host, or an http one on this machine, within its limit.
var ErrWebhookURL = errors.New("bad webhook URL")

// ErrNoWebhook reports a workspace with no webhook.
var ErrNoWebhook = errors.New("the workspace has no webhook")

// SetWebhook sets where the workspace's product hears that its sessions
// ended, starting deliveries again if a 410 Gone stopped them. The first
// time, it makes the secret deliveries are signed with and returns it, the
// only time it is seen; a URL changed later keeps its secret. Deliveries go
// over https, so no one between voice-guide and the product reads them; plain
// http is taken only for this machine, for a product run beside voice-guide.
func SetWebhook(ctx context.Context, webhooks Webhooks, workspace uuid.UUID, rawURL string) (WebhookSecret, bool, error) {
	if err := checkWebhookURL(rawURL); err != nil {
		return WebhookSecret{}, false, err
	}
	made := makeWebhookSecret()
	kept, err := webhooks.SetURL(ctx, workspace, rawURL, made)
	if err != nil {
		return WebhookSecret{}, false, fmt.Errorf("save the webhook: %w", err)
	}
	if kept.Reveal() != made.Reveal() {
		return WebhookSecret{}, false, nil
	}
	return made, true, nil
}

// checkWebhookURL refuses a URL deliveries may not go to: one over its limit,
// not an https address with a host, or an http one off this machine. Its
// errors never quote the URL.
func checkWebhookURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	switch {
	case utf8.RuneCountInString(rawURL) > maxWebhookURL:
		return fmt.Errorf("%w: at most %d characters", ErrWebhookURL, maxWebhookURL)
	case err != nil, !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://"), u.Host == "":
		return fmt.Errorf("%w: an https address with a host, such as https://example.com/webhooks", ErrWebhookURL)
	case u.Scheme == "http" && !onThisMachine(u.Hostname()):
		return fmt.Errorf("%w: https, unless the host is this machine (localhost or a loopback address)", ErrWebhookURL)
	}
	return nil
}

// onThisMachine says whether host names this machine: localhost, or a
// loopback address.
func onThisMachine(host string) bool {
	if host == "localhost" {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

// RotateWebhookSecret replaces the workspace's webhook secret and returns the
// new one, the only time it is seen: every attempt from now on is signed with
// it, and the old one signs nothing more.
func RotateWebhookSecret(ctx context.Context, webhooks Webhooks, workspace uuid.UUID) (WebhookSecret, error) {
	made := makeWebhookSecret()
	found, err := webhooks.Rotate(ctx, workspace, made)
	switch {
	case err != nil:
		return WebhookSecret{}, fmt.Errorf("rotate the webhook secret: %w", err)
	case !found:
		return WebhookSecret{}, ErrNoWebhook
	}
	return made, nil
}

// ErrWebhookStopped reports a delivery not made because its workspace's
// webhook is stopped: its product's server answered 410 Gone, to this
// delivery or an earlier one. Posting it again would change nothing until the
// URL is set again, so its job fails at once.
var ErrWebhookStopped = errors.New("deliveries to the webhook are stopped")

// Deliveries posts each ended session to its workspace's webhook.
type Deliveries struct {
	sessions    Sessions
	turns       Turns
	webhooks    Webhooks
	userContext *usercontext.Service
	poster      Poster
	// now is the clock each attempt is timed and signed by: the system's, or
	// a test's.
	now func() time.Time
}

// NewDeliveries returns the deliveries of the sessions and turns kept in the
// stores, with each session's summary from user context, to the webhooks kept
// in webhooks, through poster.
func NewDeliveries(sessions Sessions, turns Turns, webhooks Webhooks, userContext *usercontext.Service, poster Poster) *Deliveries {
	return &Deliveries{sessions: sessions, turns: turns, webhooks: webhooks, userContext: userContext, poster: poster, now: time.Now}
}

// Deliver posts the user's ended session to its workspace's webhook, built
// now, so turns saved after the end, such as a reply cut where the user
// stopped hearing it, are in it; each attempt goes to the URL and is signed
// with the secret the webhook has then. It does nothing for a workspace with
// no webhook, and one whose URL no longer meets the rule SetWebhook checks is
// ErrWebhookURL. A 2xx answer delivers it; a 410 Gone stops deliveries to the
// webhook, and that and a webhook stopped already are ErrWebhookStopped; any
// other answer, or none, is an error, to be tried again: a product's server
// that refuses a delivery now, mid-deploy or before it holds a rotated
// secret, may take it later.
func (d *Deliveries) Deliver(ctx context.Context, workspace, user uuid.UUID, args DeliveryArgs) error {
	webhook, found, err := d.webhooks.Webhook(ctx, workspace)
	switch {
	case err != nil:
		return fmt.Errorf("read the webhook: %w", err)
	case !found:
		return nil
	case webhook.Stopped:
		return fmt.Errorf("%w: session %s not delivered", ErrWebhookStopped, args.Session)
	}
	// A URL kept before the rule it must meet would post in plain text across the network.
	if err := checkWebhookURL(webhook.URL); err != nil {
		return fmt.Errorf("session %s not delivered until the webhook's URL is set again: %w", args.Session, err)
	}
	body, found, err := d.body(ctx, workspace, user, args)
	if err != nil || !found {
		return err
	}
	headers, err := signed(webhook.Secret, args.Delivery.String(), d.now(), body)
	if err != nil {
		return err
	}
	status, err := d.poster.Post(ctx, Delivery{URL: webhook.URL, Body: body, Headers: headers})
	if err != nil {
		return fmt.Errorf("post the session to the product's server: %w", err)
	}
	switch {
	case status >= 200 && status <= 299:
		return nil
	case status == http.StatusGone:
		if err := d.webhooks.Stop(ctx, workspace, webhook.URL); err != nil {
			return fmt.Errorf("stop deliveries once the product's server answered 410 Gone: %w", err)
		}
		return fmt.Errorf("%w: its server answered 410 Gone, so session %s is not delivered, nor any until the URL is set again", ErrWebhookStopped, args.Session)
	case status >= 300 && status <= 399:
		return fmt.Errorf("the product's server answered %d, a redirect, which isn't followed: the webhook's URL should be the one that answers", status)
	}
	return fmt.Errorf("the product's server answered %d", status)
}

// body builds the delivery's body from the session as saved now, and says
// whether there is a session to deliver: none for a session not found or not
// ended, which no end queued.
func (d *Deliveries) body(ctx context.Context, workspace, user uuid.UUID, args DeliveryArgs) ([]byte, bool, error) {
	session, found, err := d.sessions.ByID(ctx, workspace, user, args.Session)
	switch {
	case err != nil:
		return nil, false, fmt.Errorf("read the session: %w", err)
	case !found || !session.Ended():
		return nil, false, nil
	}
	reference, found, err := d.sessions.Reference(ctx, workspace, user)
	switch {
	case err != nil:
		return nil, false, fmt.Errorf("read the user's reference: %w", err)
	case !found:
		// Every session starts with its product naming the user, so this is a
		// fault to see in the log, never a delivery without the user.
		return nil, false, errors.New("the session's user has no reference")
	}
	ended := endedSession{
		ID: session.ID, User: reference, AgentID: session.Agent, AgentVersion: session.AgentVersion,
		StartedAt: session.StartedAt.UTC(), EndedAt: session.EndedAt.UTC(),
	}
	turns, err := d.turns.Finished(ctx, workspace, session.ID)
	if err != nil {
		return nil, false, fmt.Errorf("read the session's turns: %w", err)
	}
	summary, found, err := d.userContext.SessionSummary(ctx, workspace, user, session.ID)
	if err != nil {
		return nil, false, err
	} else if found {
		ended.Summary = &summary.Content
	}
	// The delivery runs once the summary job is done or failed for good, so a status still pending
	// is one the settling job hasn't got to yet, and none at all one ended before the status was
	// kept: either settles as that job would.
	ended.SummaryStatus, ended.SummaryData = session.SummaryStatus, session.SummaryData
	if ended.SummaryStatus != SummaryWritten && ended.SummaryStatus != SummaryNone {
		ended.SummaryStatus = settled(found)
	}
	all := make([]turnJSON, 0, len(turns))
	for _, t := range turns {
		all = append(all, turnJSON{Seq: t.Seq, Words: t.Words, Reply: t.HeardReply(), Cut: t.Status == TurnCut})
	}
	body, err := deliveryBody(deliveryJSON{ID: args.Delivery, Type: "session.ended", Timestamp: ended.EndedAt, Data: ended}, all, maxDeliveryBody)
	if err != nil {
		return nil, false, fmt.Errorf("encode the delivery: %w", err)
	}
	return body, true, nil
}

// deliveryJSON is a delivery's body: its ID, what happened, when, and the
// session it happened to.
type deliveryJSON struct {
	ID        uuid.UUID    `json:"id"`
	Type      string       `json:"type"`
	Timestamp time.Time    `json:"timestamp"`
	Data      endedSession `json:"data"`
}

// endedSession is a session as its product hears of it once ended: the user
// by the product's own reference, never voice-guide's user ID; its turns in
// order, as the user heard them; its summary, null when none was written,
// with whether one was; and its summary data, null when none was saved.
type endedSession struct {
	ID            uuid.UUID       `json:"id"`
	User          string          `json:"user"`
	AgentID       uuid.UUID       `json:"agent_id"`
	AgentVersion  int             `json:"agent_version"`
	StartedAt     time.Time       `json:"started_at"`
	EndedAt       time.Time       `json:"ended_at"`
	Turns         []turnJSON      `json:"turns"`
	TurnsLeftOut  int             `json:"turns_left_out"`
	Summary       *string         `json:"summary"`
	SummaryStatus SummaryStatus   `json:"summary_status"`
	SummaryData   json.RawMessage `json:"summary_data"`
}

// turnJSON is one turn as the user heard it: their words, and the reply as
// far as they heard it, cut when they interrupted it.
type turnJSON struct {
	Seq   int    `json:"seq"`
	Words string `json:"words"`
	Reply string `json:"reply"`
	Cut   bool   `json:"cut"`
}

// maxDeliveryBody bounds a delivery's body, in bytes, so a session of any
// length posts a body a product's server takes: at about 1 KB a turn (a
// spoken answer and a reply of a few sentences, with their JSON) and a turn
// every half minute, some two hours of conversation, while staying well under
// the 1 MiB that web servers commonly accept by default. A round guess,
// checked once products receive real sessions.
const maxDeliveryBody = 256 << 10

// deliveryBody encodes d with as many of turns as fit within limit bytes,
// the latest kept and the earliest left out first, counted in TurnsLeftOut:
// the end of a session is where it got to, and the summary covers the rest.
func deliveryBody(d deliveryJSON, turns []turnJSON, limit int) ([]byte, error) {
	d.Data.Turns, d.Data.TurnsLeftOut = []turnJSON{}, 0
	base, err := encodeJSON(d)
	if err != nil {
		return nil, err
	}
	// The body is base, the count left out's digits past its one, and the turns kept with a
	// comma between each two; each turn kept adds more than the count's digits can take away.
	size, first := len(base), len(turns)
	for first > 0 {
		t, err := encodeJSON(turns[first-1])
		if err != nil {
			return nil, err
		}
		grown := size + len(t)
		if first < len(turns) {
			grown++ // the comma before the turn after it
		}
		if grown+len(strconv.Itoa(first-1))-1 > limit {
			break
		}
		size, first = grown, first-1
	}
	d.Data.Turns, d.Data.TurnsLeftOut = turns[first:], first
	return encodeJSON(d)
}

// encodeJSON encodes v as compact JSON, leaving <, > and & as they are: the
// body is read by a server, never put in a page, and escaping them would
// only make it longer.
func encodeJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
