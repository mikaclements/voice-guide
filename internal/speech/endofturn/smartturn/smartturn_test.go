package smartturn_test

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mikaclements/voice-guide/internal/speech/endofturn/smartturn"
)

// asking returns an end-of-turn check that asks a service played by h, within deadline.
func asking(t *testing.T, deadline time.Duration, h http.HandlerFunc) *smartturn.EndOfTurn {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	check, err := smartturn.New(srv.URL+"/", deadline)
	if err != nil {
		t.Fatal(err)
	}
	return check
}

// answering plays the service with an answer it gave, recorded for a sentence Kokoro spoke.
func answering(t *testing.T, recorded string) http.HandlerFunc {
	answer, err := os.ReadFile("testdata/" + recorded)
	if err != nil {
		t.Fatalf("read the recorded answer: %v", err)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(answer)
	}
}

func TestTheServicesAnswerIsTheCheck(t *testing.T) {
	for recorded, want := range map[string]bool{"finished.json": true, "unfinished.json": false} {
		finished, err := asking(t, time.Second, answering(t, recorded)).Finished(context.Background(), make([]int16, 1600))
		if err != nil || finished != want {
			t.Errorf("%s: finished %v (%v), want %v", recorded, finished, err, want)
		}
	}
}

func TestTheLast8SecondsAreSentAsL16(t *testing.T) {
	audio := make([]int16, 9*16000)
	audio[15999], audio[16000], audio[len(audio)-1] = 7, 1, 1000
	var body []byte
	check := asking(t, time.Second, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/turn/check" || r.Header.Get("Content-Type") != "audio/L16;rate=16000" {
			t.Errorf("asked %s %s as %q, want POST /v1/turn/check as audio/L16;rate=16000", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		body, _ = io.ReadAll(r.Body)
		answering(t, "finished.json")(w, r)
	})
	if _, err := check.Finished(context.Background(), audio); err != nil {
		t.Fatal(err)
	}
	if len(body) != 2*8*16000 {
		t.Fatalf("sent %d bytes, want the last 8 s, %d bytes", len(body), 2*8*16000)
	}
	// L16 is in network byte order; the 7 in the first second is left out.
	if first, last := binary.BigEndian.Uint16(body), binary.BigEndian.Uint16(body[len(body)-2:]); first != 1 || last != 1000 {
		t.Errorf("sent samples from %d to %d, want from 1 to 1000", first, last)
	}
}

func TestACheckPastItsDeadlineFails(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	check := asking(t, 50*time.Millisecond, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	started := time.Now()
	if _, err := check.Finished(context.Background(), make([]int16, 1600)); err == nil || time.Since(started) > time.Second {
		t.Fatalf("a check the service never answered returned %v after %s, want a failure at the deadline", err, time.Since(started))
	}
}

func TestARefusalFailsWithItsStatusNotItsBody(t *testing.T) {
	check := asking(t, time.Second, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "what was said", http.StatusInternalServerError)
	})
	_, err := check.Finished(context.Background(), make([]int16, 1600))
	if err == nil || !strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "what was said") {
		t.Fatalf("got %v, want a failure naming the status and not the body", err)
	}
}

func TestNoAudioIsNotFinishedAndAsksNothing(t *testing.T) {
	check := asking(t, time.Second, func(http.ResponseWriter, *http.Request) { t.Error("asked about no audio") })
	if finished, err := check.Finished(context.Background(), nil); finished || err != nil {
		t.Fatalf("no audio answered %v (%v), want not finished", finished, err)
	}
}

func TestAnAnswerThatIsNotOneFailsTheCheck(t *testing.T) {
	for name, body := range map[string]string{"not JSON": "finished", "too long": `{"x": "` + strings.Repeat("x", 2048) + `", "finished": true}`} {
		check := asking(t, time.Second, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
		if _, err := check.Finished(context.Background(), make([]int16, 1600)); err == nil {
			t.Errorf("%s: the check took it", name)
		}
	}
}

func TestAServiceThatCantBeReachedFailsTheCheck(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	check, _ := smartturn.New(srv.URL, time.Second)
	if _, err := check.Finished(context.Background(), make([]int16, 1600)); err == nil {
		t.Fatal("a check of a service that isn't there passed")
	}
}

func TestOnlyHTTPAddressesAreTaken(t *testing.T) {
	if _, err := smartturn.New("ws://localhost:8092", time.Second); err == nil {
		t.Error("New took a ws address")
	}
}
