package http_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/conversation"
	modelhttp "github.com/mikaclements/voice-guide/internal/conversation/model/http"
)

var request = conversation.ModelRequest{Model: "claude-test", Session: uuid.MustParse("01a1de8a-0000-7000-8000-0000000000c1"), Prompt: conversation.Prompt{
	StableLayers: []string{"You keep a lighthouse."},
	Conversation: []conversation.Message{{Role: conversation.RoleUser, Text: "Evening."}, {Role: conversation.RoleAgent, Text: "Evening.\n"}},
	NewWords:     "Is the lamp lit tonight?",
}}

// recorded returns the lines of a reply recorded from the model service.
func recorded(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("testdata/reply.ndjson")
	if err != nil {
		t.Fatalf("read the recorded reply: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// replaying returns a model service that answers every request with lines.
func replaying(t *testing.T, lines []string) *modelhttp.Model {
	return serving(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, l := range lines {
			_, _ = io.WriteString(w, l+"\n")
		}
	})
}

func serving(t *testing.T, h http.HandlerFunc) *modelhttp.Model {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return modelhttp.New(srv.URL + "/")
}

func collect(events <-chan conversation.ModelEvent) []conversation.ModelEvent {
	var all []conversation.ModelEvent
	for e := range events {
		all = append(all, e)
	}
	return all
}

func texts(pieces ...string) []conversation.ModelEvent {
	events := make([]conversation.ModelEvent, 0, len(pieces))
	for _, p := range pieces {
		events = append(events, conversation.ModelEvent{Text: p})
	}
	return events
}

func TestAReplyArrivesAsTextInOrderThenDoneWithTheModelServicesReport(t *testing.T) {
	var asked map[string]any
	m := serving(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/replies" || json.NewDecoder(r.Body).Decode(&asked) != nil {
			t.Errorf("request %s %s, want a JSON POST to /v1/replies", r.Method, r.URL.Path)
		}
		for _, l := range recorded(t) {
			_, _ = io.WriteString(w, l+"\n")
		}
	})

	got := collect(m.Reply(context.Background(), request))
	first, total := 1159*time.Millisecond, 1525*time.Millisecond
	want := append(texts("Yes, the lamp", " is lit and", " its beam is s", "weeping the d", "ark water", " tonight."), conversation.ModelEvent{Done: true, Report: &conversation.ModelReport{
		Provider: "claude-subscription", Model: "claude-sonnet-5-5",
		Usage: &conversation.ModelUsage{Input: 2, CachedInput: 0, CacheWrite: 618, Output: 30}, FirstText: &first, Total: total,
	}})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events = %+v, want %+v", got, want)
	}
	wantAsked := map[string]any{
		"model": "claude-test", "session_id": "01a1de8a-0000-7000-8000-0000000000c1", "stable_layers": []any{"You keep a lighthouse."},
		"conversation": []any{map[string]any{"role": "user", "text": "Evening."}, map[string]any{"role": "agent", "text": "Evening.\n"}},
		"new_words":    "Is the lamp lit tonight?",
	}
	if !reflect.DeepEqual(asked, wantAsked) {
		t.Errorf("asked %v, want %v", asked, wantAsked)
	}
}

func TestAnAskOutsideAnySessionSendsNoSessionID(t *testing.T) {
	var asked map[string]any
	m := serving(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&asked)
		for _, l := range recorded(t) {
			_, _ = io.WriteString(w, l+"\n")
		}
	})
	outside := request
	outside.Session = uuid.Nil()
	collect(m.Reply(context.Background(), outside))
	if _, sent := asked["session_id"]; sent || asked["new_words"] != request.NewWords {
		t.Errorf("asked %v, want the request without session_id, so the model service keeps nothing for it", asked)
	}
}

func TestNewWordsSavedOtherwiseThanSentAreSentToo(t *testing.T) {
	var asked []map[string]any
	m := serving(t, func(w http.ResponseWriter, r *http.Request) {
		var one map[string]any
		_ = json.NewDecoder(r.Body).Decode(&one)
		asked = append(asked, one)
		for _, l := range recorded(t) {
			_, _ = io.WriteString(w, l+"\n")
		}
	})
	withBoard := request
	withBoard.NewWords = "<board>\nTwo boxes.\n</board>\n\nLike this?"
	withBoard.NewWordsSaved = "(The user's board changed here.)\n\nLike this?"
	plain := request
	plain.NewWordsSaved = plain.NewWords
	collect(m.Reply(context.Background(), withBoard))
	collect(m.Reply(context.Background(), plain))
	if len(asked) != 2 || asked[0]["new_words_saved"] != withBoard.NewWordsSaved {
		t.Errorf("asked %v, want new_words_saved sent with a board", asked)
	}
	if _, sent := asked[1]["new_words_saved"]; sent {
		t.Errorf("asked %v, want no new_words_saved for words saved as sent", asked[1])
	}
}

func TestARefusalAtTheModelServicesCapIsBusyAndAnyOther503Overloaded(t *testing.T) {
	for problem, want := range map[string]conversation.ModelErrorKind{
		`{"type":"/v1/problems/at-capacity","title":"At capacity","status":503}`: "busy",
		`{"type":"about:blank","title":"Service Unavailable","status":503}`:      "overloaded",
		`not a problem document`: "overloaded",
	} {
		m := serving(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, problem)
		})
		if got := collect(m.Reply(context.Background(), request)); len(got) != 1 || got[0].Error != want {
			t.Errorf("503 with %s gave %+v, want the error %s", problem, got, want)
		}
	}
}

func TestAnErrorEventEndsTheReplyWithItsKind(t *testing.T) {
	got := collect(replaying(t, append(recorded(t)[:2], `{"type":"error","kind":"rate_limited"}`)).Reply(context.Background(), request))
	if want := append(texts("Yes, the lamp", " is lit and"), conversation.ModelEvent{Error: "rate_limited"}); !reflect.DeepEqual(got, want) {
		t.Errorf("events = %+v, want %+v", got, want)
	}
}

func TestAConversationGrownTooLargeKeepsItsOwnKind(t *testing.T) {
	got := collect(replaying(t, []string{recorded(t)[0], `{"type":"error","kind":"too_large"}`}).Reply(context.Background(), request))
	if want := append(texts("Yes, the lamp"), conversation.ModelEvent{Error: "too_large"}); !reflect.DeepEqual(got, want) {
		t.Errorf("events = %+v, want the first text, then too_large", got)
	}
}

func TestAnErrorEventWithoutAKindFromTheClosedSetIsUnknown(t *testing.T) {
	for name, line := range map[string]string{
		"a kind outside the set": `{"type":"error","kind":"solar_flare"}`,
		"no kind":                `{"type":"error"}`,
	} {
		got := collect(replaying(t, []string{recorded(t)[0], line}).Reply(context.Background(), request))
		if want := append(texts("Yes, the lamp"), conversation.ModelEvent{Error: "unknown"}); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: events = %+v, want the first text, then unknown", name, got)
		}
	}
}

func TestAMalformedLineEndsTheReplyAsUnknown(t *testing.T) {
	lines := recorded(t)
	got := collect(replaying(t, []string{lines[0], `{"type":"text","text":`, lines[1], lines[6]}).Reply(context.Background(), request))
	if want := append(texts("Yes, the lamp"), conversation.ModelEvent{Error: "unknown"}); !reflect.DeepEqual(got, want) {
		t.Errorf("events = %+v, want the first text, then unknown", got)
	}
}

// The model service may add events; voice reads past the ones it doesn't know.
func TestAnEventOfAnUnknownTypeIsSkipped(t *testing.T) {
	lines := recorded(t)
	got := collect(replaying(t, append([]string{`{"type":"thinking","text":"Lamps."}`}, lines...)).Reply(context.Background(), request))
	if len(got) != len(lines) || got[0].Text != "Yes, the lamp" || !got[len(got)-1].Done {
		t.Errorf("events = %+v, want the recorded reply, whole", got)
	}
}

// The reply arrived whole, so a report that doesn't read leaves it complete,
// with no report rather than a wrong one.
func TestADoneEventWhoseReportDoesntReadStillEndsTheReply(t *testing.T) {
	lines := recorded(t)
	for name, done := range map[string]string{
		"usage of another type":  `{"type":"done","provider":"claude-subscription","model":"claude-sonnet-5-5","usage":{"input_tokens":"2"},"timing":{"total_ms":1525}}`,
		"timing of another type": `{"type":"done","provider":"claude-subscription","model":"claude-sonnet-5-5","usage":null,"timing":{"first_text_ms":"soon","total_ms":1525}}`,
		"no provider":            `{"type":"done","model":"claude-sonnet-5-5","usage":null,"timing":{"total_ms":1525}}`,
		"no model":               `{"type":"done","provider":"claude-subscription","usage":null,"timing":{"total_ms":1525}}`,
		"no total":               `{"type":"done","provider":"claude-subscription","model":"claude-sonnet-5-5","usage":null,"timing":{}}`,
	} {
		got := collect(replaying(t, append(lines[:6:6], done)).Reply(context.Background(), request))
		if want := append(texts("Yes, the lamp", " is lit and", " its beam is s", "weeping the d", "ark water", " tonight."), conversation.ModelEvent{Done: true}); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: events = %+v, want every text, then done with no report", name, got)
		}
	}
}

func TestAStreamCutMidwayThroughALineIsUnavailable(t *testing.T) {
	for name, cut := range map[string]func(){
		"the stream ends":       func() {},
		"the connection breaks": func() { panic(http.ErrAbortHandler) },
	} {
		m := serving(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, recorded(t)[0]+"\n"+`{"type":"text","text":" is l`)
			w.(http.Flusher).Flush()
			cut()
		})
		got := collect(m.Reply(context.Background(), request))
		if want := append(texts("Yes, the lamp"), conversation.ModelEvent{Error: "unavailable"}); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: events = %+v, want the first text, then unavailable", name, got)
		}
	}
}

func TestAStreamCutBeforeItsFinalEventIsUnavailable(t *testing.T) {
	lines := recorded(t)
	got := collect(replaying(t, lines[:len(lines)-1]).Reply(context.Background(), request))
	if last := got[len(got)-1]; len(got) != len(lines) || last.Error != "unavailable" {
		t.Errorf("events = %+v, want every text, then unavailable", got)
	}
}

func TestALineOverTheCapIsRefused(t *testing.T) {
	long := `{"type":"text","text":"` + strings.Repeat("a", 64<<10) + `"}`
	got := collect(replaying(t, []string{recorded(t)[0], long, recorded(t)[6]}).Reply(context.Background(), request))
	if want := append(texts("Yes, the lamp"), conversation.ModelEvent{Error: "unknown"}); !reflect.DeepEqual(got, want) {
		t.Errorf("events = %+v, want the first text, then unknown", got)
	}
}

func TestARefusedRequestBecomesTheErrorKindItsStatusMeans(t *testing.T) {
	for status, kind := range map[int]conversation.ModelErrorKind{
		http.StatusServiceUnavailable:    "overloaded",
		http.StatusBadRequest:            "bad_request",
		http.StatusRequestEntityTooLarge: "too_large",
		http.StatusInternalServerError:   "unknown",
	} {
		m := serving(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
		if got := collect(m.Reply(context.Background(), request)); len(got) != 1 || got[0].Error != kind {
			t.Errorf("status %d: events = %+v, want only %s", status, got, kind)
		}
	}
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	if got := collect(modelhttp.New(gone.URL).Reply(context.Background(), request)); len(got) != 1 || got[0].Error != "unavailable" {
		t.Errorf("unreachable: events = %+v, want only unavailable", got)
	}
}

func TestCancellingAReplyClosesTheConnection(t *testing.T) {
	closed, ended := make(chan struct{}), make(chan struct{})
	m := serving(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, recorded(t)[0]+"\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done(): // the server sees the connection close
			close(closed)
		case <-ended: // the test failed; let the server shut down
		}
	})
	t.Cleanup(func() { close(ended) })
	ctx, cancel := context.WithCancel(context.Background())
	events := m.Reply(ctx, request)
	if first := <-events; first.Text != "Yes, the lamp" {
		t.Fatalf("first event = %+v, want the first text", first)
	}
	cancel()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the model service's connection stayed open after the reply was cancelled")
	}
	if rest := collect(events); len(rest) != 0 {
		t.Errorf("events after cancelling = %+v, want none", rest)
	}
}

// A model service that stops sending holds the reply only until the turn's
// deadline: the reply then ends with nothing more, which the turn saves as
// lost, and the connection closes.
func TestASilentStreamEndsAtTheDeadline(t *testing.T) {
	closed, ended := make(chan struct{}), make(chan struct{})
	m := serving(t, func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(closed)
		case <-ended:
		}
	})
	t.Cleanup(func() { close(ended) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan []conversation.ModelEvent)
	go func() { done <- collect(m.Reply(ctx, request)) }()
	select {
	case got := <-done:
		if len(got) != 0 {
			t.Errorf("events = %+v, want none", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reply was still open long after its deadline")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the model service's connection stayed open after the deadline")
	}
}
