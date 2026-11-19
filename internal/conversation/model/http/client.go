// Package http reaches the model service over HTTP: conversation's model adapter. It
// asks for one reply per request and reads the reply's events, one JSON object
// per line, as they stream in.
package http

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/conversation"
)

// maxLineBytes bounds one event of a reply. A text event holds a few words and
// the final event a few numbers, so a longer line means a broken stream, and
// reading on would only fill memory.
const maxLineBytes = 64 << 10

// Model asks the model service for replies.
type Model struct {
	url    string
	client *http.Client
}

// New returns a model that asks the model service at baseURL, such as
// http://127.0.0.1:8091. Each reply is bounded by its context's deadline alone.
func New(baseURL string) *Model {
	return &Model{url: strings.TrimSuffix(baseURL, "/") + "/v1/replies", client: &http.Client{}}
}

// Reply asks for the reply to r and sends its events as they arrive. The
// request carries ctx's deadline, and cancelling ctx closes the connection,
// which stops the model service's reply too.
func (m *Model) Reply(ctx context.Context, r conversation.ModelRequest) <-chan conversation.ModelEvent {
	events := make(chan conversation.ModelEvent)
	go func() {
		defer close(events)
		send := func(e conversation.ModelEvent) bool {
			if ctx.Err() != nil { // a caller draining a stopped reply gets nothing more
				return false
			}
			select {
			case events <- e:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if kind := m.reply(ctx, r, send); kind != "" {
			send(conversation.ModelEvent{Error: kind})
		}
	}()
	return events
}

// reply sends the reply's text and done events, and returns why it failed
// instead, if it did.
func (m *Model) reply(ctx context.Context, r conversation.ModelRequest, send func(conversation.ModelEvent) bool) conversation.ModelErrorKind {
	body, err := json.Marshal(requestOf(r))
	if err != nil {
		return conversation.ModelUnknown
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.url, bytes.NewReader(body))
	if err != nil {
		return conversation.ModelUnknown
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return conversation.ModelUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return refused(resp)
	}
	lines := bufio.NewScanner(resp.Body)
	lines.Buffer(make([]byte, 0, 4<<10), maxLineBytes)
	lines.Split(wholeLines)
	for lines.Scan() {
		var e eventJSON
		if err := json.Unmarshal(lines.Bytes(), &e); err != nil {
			return conversation.ModelUnknown
		}
		switch e.Type {
		case "text":
			if !send(conversation.ModelEvent{Text: e.Text}) {
				return ""
			}
		case "done":
			send(conversation.ModelEvent{Done: true, Report: reportOf(lines.Bytes())})
			return ""
		case "error":
			return kindOf(e.Kind)
		}
		// Any other type is one added after this client was written; skipping
		// it lets the model service add events without breaking voice.
	}
	if errors.Is(lines.Err(), bufio.ErrTooLong) {
		return conversation.ModelUnknown
	}
	// The stream ended, or the connection broke, before the final event,
	// perhaps midway through a line.
	return conversation.ModelUnavailable
}

// wholeLines splits a stream into lines, refusing a last line without its
// line break: the stream was cut midway through it, so it isn't an event.
func wholeLines(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) > 0 && bytes.IndexByte(data, '\n') < 0 {
		return 0, nil, io.ErrUnexpectedEOF
	}
	return bufio.ScanLines(data, atEOF)
}

// kindOf says why a reply failed, from the model service's closed set. A kind
// outside it, or none, is unknown, so voice never acts on a reason it doesn't
// know.
func kindOf(kind string) conversation.ModelErrorKind {
	switch k := conversation.ModelErrorKind(kind); k {
	case conversation.ModelRateLimited, conversation.ModelQuota, conversation.ModelOverloaded, conversation.ModelUnavailable,
		conversation.ModelAuth, conversation.ModelBadRequest, conversation.ModelTooLarge:
		return k
	}
	return conversation.ModelUnknown
}

// refused says why the model service refused a request: at its own cap of
// replies at once (busy), which moments may clear; overloaded otherwise; a conversation grown past
// its size limit, which only shortening the conversation fixes; or a request
// it can't take, which no retry fixes.
func refused(resp *http.Response) conversation.ModelErrorKind {
	status := resp.StatusCode
	switch {
	case status == http.StatusServiceUnavailable && problemType(resp) == atCapacity:
		return conversation.ModelBusy
	case status == http.StatusServiceUnavailable:
		return conversation.ModelOverloaded
	case status == http.StatusRequestEntityTooLarge:
		return conversation.ModelTooLarge
	case status >= 400 && status < 500:
		return conversation.ModelBadRequest
	}
	return conversation.ModelUnknown
}

// atCapacity is the problem type the model service refuses with at its own
// cap on replies: no place free, which moments may clear.
const atCapacity = "/v1/problems/at-capacity"

// problemType reads a refusal's problem type, from at most a few KiB of its
// body: none when it sent no problem document.
func problemType(resp *http.Response) string {
	var p struct {
		Type string `json:"type"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&p) != nil {
		return ""
	}
	return p.Type
}

type requestJSON struct {
	Model string `json:"model"`
	// Left out for an ask outside any session, so the model service keeps nothing for it.
	SessionID    string        `json:"session_id,omitempty"`
	StableLayers []string      `json:"stable_layers"`
	Conversation []messageJSON `json:"conversation"`
	NewWords     string        `json:"new_words"`
}

type messageJSON struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

func requestOf(r conversation.ModelRequest) requestJSON {
	out := requestJSON{
		Model: r.Model, StableLayers: r.StableLayers,
		Conversation: make([]messageJSON, 0, len(r.Conversation)), NewWords: r.NewWords,
	}
	if r.Session != uuid.Nil() {
		out.SessionID = r.Session.String()
	}
	for _, m := range r.Conversation {
		out.Conversation = append(out.Conversation, messageJSON{Role: string(m.Role), Text: m.Text})
	}
	return out
}

type eventJSON struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Kind string `json:"kind"`
}

// reportJSON is the report in a done event.
type reportJSON struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Usage    *struct {
		Input       int `json:"input_tokens"`
		CachedInput int `json:"cached_input_tokens"`
		CacheWrite  int `json:"cache_write_tokens"`
		Output      int `json:"output_tokens"`
	} `json:"usage"`
	Timing struct {
		FirstTextMS *int64 `json:"first_text_ms"`
		TotalMS     *int64 `json:"total_ms"`
	} `json:"timing"`
}

// reportOf reads the report in a done event's line. A report that doesn't
// read, or lacks who answered or the total time every report holds, is left
// out, never the reply it ends: the reply arrived whole, and no report shows
// it unmeasured rather than measured wrong.
func reportOf(line []byte) *conversation.ModelReport {
	var e reportJSON
	if json.Unmarshal(line, &e) != nil || e.Provider == "" || e.Model == "" || e.Timing.TotalMS == nil {
		return nil
	}
	r := &conversation.ModelReport{Provider: e.Provider, Model: e.Model, Total: time.Duration(*e.Timing.TotalMS) * time.Millisecond}
	if e.Usage != nil {
		u := conversation.ModelUsage(*e.Usage)
		r.Usage = &u
	}
	if e.Timing.FirstTextMS != nil {
		d := time.Duration(*e.Timing.FirstTextMS) * time.Millisecond
		r.FirstText = &d
	}
	return r
}
