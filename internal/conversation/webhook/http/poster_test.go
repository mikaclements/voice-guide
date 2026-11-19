package http_test

import (
	"context"
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mikaclements/voice-guide/internal/conversation"
	webhookhttp "github.com/mikaclements/voice-guide/internal/conversation/webhook/http"
)

func delivery(url string) conversation.Delivery {
	return conversation.Delivery{URL: url, Body: []byte(`{"type":"session.ended","data":{"words":"Is the lamp <lit>?"}}`), Headers: map[string]string{
		"webhook-id": "01a1de8a-0000-7000-8000-0000000000f1", "webhook-timestamp": "1763457600", "webhook-signature": "v1,c2lnbmVk",
	}}
}

func TestADeliveryIsPostedAsJSONWithItsHeadersAndTheStatusComesBack(t *testing.T) {
	var got *nethttp.Request
	var gotBody []byte
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		got, gotBody = r, mustRead(t, r.Body)
		w.WriteHeader(nethttp.StatusAccepted)
	}))
	defer server.Close()
	d := delivery(server.URL + "/webhooks")

	status, err := webhookhttp.New(webhookhttp.Deadline).Post(context.Background(), d)
	if err != nil || status != nethttp.StatusAccepted {
		t.Fatalf("Post = %d, %v; want the server's 202", status, err)
	}
	switch {
	case got.Method != nethttp.MethodPost || got.URL.Path != "/webhooks" || got.Header.Get("Content-Type") != "application/json":
		t.Errorf("request %s %s, %q; want a JSON POST to the webhook's path", got.Method, got.URL.Path, got.Header.Get("Content-Type"))
	case string(gotBody) != string(d.Body):
		t.Errorf("body %s, want it as given", gotBody)
	}
	for name, value := range d.Headers {
		if got.Header.Get(name) != value {
			t.Errorf("%s: %q, want %q, as given", name, got.Header.Get(name), value)
		}
	}
}

func TestARedirectIsAnsweredAsItIsNotFollowed(t *testing.T) {
	var followed atomic.Int32
	elsewhere := httptest.NewServer(nethttp.HandlerFunc(func(nethttp.ResponseWriter, *nethttp.Request) { followed.Add(1) }))
	defer elsewhere.Close()
	server := httptest.NewServer(nethttp.RedirectHandler(elsewhere.URL, nethttp.StatusTemporaryRedirect))
	defer server.Close()

	status, err := webhookhttp.New(webhookhttp.Deadline).Post(context.Background(), delivery(server.URL))
	if err != nil || status != nethttp.StatusTemporaryRedirect || followed.Load() != 0 {
		t.Errorf("Post = %d, %v, with %d posts where it pointed; want the 307 itself, nothing posted elsewhere", status, err, followed.Load())
	}
}

func TestAServerThatAnswersNothingWithinTheDeadlineIsAnErrorWithoutItsAddress(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(nethttp.HandlerFunc(func(nethttp.ResponseWriter, *nethttp.Request) {
		<-release // answers only once the poster has given up
	}))
	defer server.Close()
	defer close(release)
	url := server.URL + "/webhooks?token=harbour-master"

	status, err := webhookhttp.New(50*time.Millisecond).Post(context.Background(), delivery(url))
	if err == nil || status != 0 || strings.Contains(err.Error(), "harbour-master") || !strings.Contains(err.Error(), "within the deadline") {
		t.Errorf("Post = %d, %v; want an error saying no answer came in time, without the URL", status, err)
	}
}

func TestConnectingGivesUpAfterItsOwnTimeoutWellWithinTheAttemptsDeadline(t *testing.T) {
	// A server that never accepts the connection: the dial waits until it is given up.
	never := func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	status, err := webhookhttp.NewDialing(5*time.Second, 20*time.Millisecond, never).Post(context.Background(), delivery("https://jobs.example/webhooks"))
	if err == nil || status != 0 || !strings.Contains(err.Error(), "couldn't be reached") {
		t.Errorf("Post = %d, %v; want it given up at connecting, as unreachable, not at the attempt's deadline", status, err)
	}
	if webhookhttp.ConnectTimeout <= 0 || webhookhttp.ConnectTimeout >= webhookhttp.Deadline {
		t.Errorf("connecting is given %s, want a part of the attempt's %s", webhookhttp.ConnectTimeout, webhookhttp.Deadline)
	}
}

func TestAServerThatCantBeReachedIsAnErrorWithoutItsAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	url := "http://" + ln.Addr().String() + "/webhooks?token=harbour-master"
	_ = ln.Close() // nothing listens there now

	status, err := webhookhttp.New(webhookhttp.Deadline).Post(context.Background(), delivery(url))
	if err == nil || status != 0 || strings.Contains(err.Error(), "harbour-master") || strings.Contains(err.Error(), ln.Addr().String()) {
		t.Errorf("Post = %d, %v; want an error without the URL", status, err)
	}
}

func mustRead(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Errorf("read the body: %v", err)
	}
	return b
}

var _ conversation.Poster = (*webhookhttp.Poster)(nil)
