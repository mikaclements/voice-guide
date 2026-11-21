package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/agents/store/fake"
)

// voiceList is a fake of the text-to-speech service's voices: it answers names, or err.
type voiceList struct {
	names []string
	err   error
}

func (l *voiceList) Voices(context.Context) ([]string, error) { return l.names, l.err }

// keeperFile is a valid agent file for the keeper; extra settings go after its own.
func keeperFile(extra, persona string) string {
	return "---\nid: " + keeperID.String() + "\nname: Lighthouse keeper\nreads: [profile]\n" + extra + "---\n" + persona
}

// put sends a PUT of body to path, as a product's server would: bearer is its key, and contentType
// is sent unless empty.
func (f fixture) put(path, bearer, contentType string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest("PUT", path, body)
	req.Header.Set("Authorization", "Bearer "+bearer)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	f.rt.ServeHTTP(rec, req)
	return rec
}

// putFile publishes file as the keeper with a key that may.
func (f fixture) putFile(t *testing.T, key, file string) *httptest.ResponseRecorder {
	t.Helper()
	return f.put("/v1/agents/"+keeperID.String(), key, "text/markdown", strings.NewReader(file))
}

type published struct {
	ID           string
	Version      int
	Persona      string
	Voice        string
	VoiceChecked bool `json:"voice_checked"`
}

func decodePublished(t *testing.T, rec *httptest.ResponseRecorder) published {
	t.Helper()
	var got published
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	return got
}

func TestAnAgentFilePublishesAsANewVersionAndTheSameFileAgainAnswersTheSavedOne(t *testing.T) {
	store := fake.New()
	f := newFixture(t, store)
	key := f.keyWith(t, access.PublishAgents)
	file := keeperFile("voice: bf_emma\n", "You keep the light.\n")

	rec := f.putFile(t, key, file)
	first := decodePublished(t, rec)
	if rec.Code != nethttp.StatusCreated || first.ID != keeperID.String() || first.Version != 1 || first.Persona != "You keep the light.\n" || first.Voice != "bf_emma" || !first.VoiceChecked {
		t.Errorf("first PUT = %d %+v; want 201, version 1, voice checked", rec.Code, first)
	}
	rec = f.putFile(t, key, file)
	if again := decodePublished(t, rec); rec.Code != nethttp.StatusOK || again.Version != 1 {
		t.Errorf("same file again = %d %+v; want 200, version 1", rec.Code, again)
	}
	rec = f.putFile(t, key, keeperFile("voice: bf_emma\n", "You keep the light burning.\n"))
	if changed := decodePublished(t, rec); rec.Code != nethttp.StatusCreated || changed.Version != 2 {
		t.Errorf("changed file = %d %+v; want 201, version 2", rec.Code, changed)
	}
	// What the next session start reads, and read only in the key's workspace.
	if v, err := agents.NewCatalog(store).Newest(context.Background(), workspace, keeperID); err != nil || v.Number != 2 || !strings.Contains(v.Persona, "burning") {
		t.Errorf("newest = %+v, %v; want the published second version", v, err)
	}
	if got, _ := store.Newest(context.Background(), elsewhere); len(got) != 0 {
		t.Errorf("another workspace holds %d agents; want none", len(got))
	}
}

func TestTheFilesMediaTypeMayCarryParametersAndAnyOtherIsRefused(t *testing.T) {
	f := newFixture(t, fake.New())
	key := f.keyWith(t, access.PublishAgents)
	path := "/v1/agents/" + keeperID.String()
	body := keeperFile("", "You keep the light.\n")
	if rec := f.put(path, key, "text/markdown; charset=utf-8", strings.NewReader(body)); rec.Code != nethttp.StatusCreated {
		t.Errorf("text/markdown; charset=utf-8 = %d; want 201", rec.Code)
	}
	for _, contentType := range []string{"", "application/json", "text/plain", "application/x-www-form-urlencoded", "text/markdown; charset"} {
		if rec := f.put(path, key, contentType, strings.NewReader(body)); rec.Code != nethttp.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q = %d; want 415", contentType, rec.Code)
		}
	}
}

// countingReader counts what the server reads of an endless body.
type countingReader struct{ read int }

func (c *countingReader) Read(p []byte) (int, error) {
	c.read += len(p)
	return len(p), nil
}

func TestABodyOverTheLimitIsRefusedWithoutReadingMuchPastIt(t *testing.T) {
	f := newFixture(t, fake.New())
	body := &countingReader{}
	rec := f.put("/v1/agents/"+keeperID.String(), f.keyWith(t, access.PublishAgents), "text/markdown", body)
	if rec.Code != nethttp.StatusRequestEntityTooLarge {
		t.Errorf("an endless body = %d; want 413", rec.Code)
	}
	if body.read > 256<<10+1 {
		t.Errorf("read %d bytes of an endless body; want it stopped at the 256 KiB limit", body.read)
	}
	file := keeperFile("", strings.Repeat("a", 256<<10-len(keeperFile("", ""))))
	if rec := f.putFile(t, f.keyWith(t, access.PublishAgents), file); rec.Code != nethttp.StatusCreated {
		t.Errorf("a file of exactly 256 KiB = %d; want 201", rec.Code)
	}
}

func TestAFileTheChecksRefuseIsUnprocessableAndNamesTheProblem(t *testing.T) {
	f := newFixture(t, fake.New())
	key := f.keyWith(t, access.PublishAgents)
	other := uuid.NewV7().String()
	tests := map[string]struct{ body, want string }{
		"an empty body":               {"", "empty"},
		"not UTF-8":                   {keeperFile("", "caf\xe9\n"), "UTF-8"},
		"no settings block":           {"You keep the light.\n", "settings block"},
		"an unknown setting":          {keeperFile("colour: red\n", "You keep the light.\n"), "colour"},
		"no persona":                  {keeperFile("", ""), "no persona"},
		"a kind user context lacks":   {strings.Replace(keeperFile("", "x\n"), "[profile]", "[diary]", 1), `reads "diary"`},
		"a speed out of range":        {keeperFile("speed: 3\n", "x\n"), "speed"},
		"an id that isn't the path's": {strings.Replace(keeperFile("", "x\n"), keeperID.String(), other, 1), "isn't"},
		"no id":                       {strings.Replace(keeperFile("", "x\n"), "id: "+keeperID.String()+"\n", "", 1), "id"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := f.putFile(t, key, tt.body)
			var p problem
			if err := json.NewDecoder(rec.Body).Decode(&p); err != nil || rec.Code != nethttp.StatusUnprocessableEntity || !strings.Contains(p.Detail, tt.want) {
				t.Errorf("%d %+v (%v); want 422 mentioning %q", rec.Code, p, err, tt.want)
			}
		})
	}
	if rec := f.put("/v1/agents/keeper", key, "text/markdown", strings.NewReader(keeperFile("", "x\n"))); rec.Code != nethttp.StatusUnprocessableEntity {
		t.Errorf("a path ID that isn't a UUID = %d; want 422", rec.Code)
	}
}

func TestAVoiceTheTextToSpeechServiceLacksIsAProblemOfItsOwnType(t *testing.T) {
	f := newFixture(t, fake.New())
	rec := f.putFile(t, f.keyWith(t, access.PublishAgents), keeperFile("voice: am_adam\n", "x\n"))
	var p problem
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil || rec.Code != nethttp.StatusUnprocessableEntity || p.Type != "/v1/problems/unknown-voice" || !strings.Contains(p.Detail, "am_adam") {
		t.Errorf("%d %+v (%v); want 422 of type unknown-voice naming the voice", rec.Code, p, err)
	}
}

func TestAnAgentPublishesUncheckedWhenTheTextToSpeechServiceIsDown(t *testing.T) {
	f := newFixtureWithVoices(t, fake.New(), &voiceList{err: errors.New("connection refused")})
	rec := f.putFile(t, f.keyWith(t, access.PublishAgents), keeperFile("voice: am_adam\n", "x\n"))
	if got := decodePublished(t, rec); rec.Code != nethttp.StatusCreated || got.VoiceChecked || got.Version != 1 {
		t.Errorf("%d %+v; want 201 with voice_checked false", rec.Code, got)
	}
}

func TestAnAgentIDAnotherWorkspaceOwnsIsAConflictThatSaysToPickANewOne(t *testing.T) {
	store := fake.New()
	publish(t, store, elsewhere, agents.Agent{ID: keeperID, Name: "Harbour pilot", Persona: "You bring ships in."})
	f := newFixture(t, store)
	rec := f.putFile(t, f.keyWith(t, access.PublishAgents), keeperFile("", "x\n"))
	var p problem
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil || rec.Code != nethttp.StatusConflict || !strings.Contains(p.Detail, "new ID") || strings.Contains(rec.Body.String(), "pilot") {
		t.Errorf("%d %+v (%v); want 409 saying to pick a new ID, without the other agent", rec.Code, p, err)
	}
}

func TestPublishingNeedsAKeyThatMayPublish(t *testing.T) {
	f := newFixture(t, fake.New())
	file := keeperFile("", "x\n")
	if rec := f.putFile(t, f.keyWith(t, access.RunSessions), file); rec.Code != nethttp.StatusForbidden {
		t.Errorf("a key that only runs sessions: %d; want 403", rec.Code)
	}
	tk, err := f.key.MintTicket(access.Caller{Workspace: workspace, User: uuid.NewV7(), Session: uuid.NewV7()}, time.Now(), time.Now())
	if err != nil {
		t.Fatalf("MintTicket: %v", err)
	}
	if rec := f.putFile(t, tk.Reveal(), file); rec.Code != nethttp.StatusUnauthorized {
		t.Errorf("a ticket: %d; want 401", rec.Code)
	}
	req := httptest.NewRequest("PUT", "/v1/agents/"+keeperID.String(), strings.NewReader(file))
	req.Header.Set("Authorization", "Bearer "+f.keyWith(t, access.PublishAgents))
	req.Header.Set("Content-Type", "text/markdown")
	req.Header.Set("Origin", "https://harbour.example")
	rec := httptest.NewRecorder()
	f.rt.ServeHTTP(rec, req)
	if rec.Code < 400 {
		t.Errorf("a key sent with an Origin: %d; want it refused", rec.Code)
	}
	if got, _ := agents.NewCatalog(f.store).Available(context.Background(), workspace); len(got) != 0 {
		t.Errorf("a refused publish saved %d agents", len(got))
	}
}

// savingFails is a store that reads fine and can't save, as a database that went down.
type savingFails struct{ agents.Versions }

func (savingFails) Save(context.Context, uuid.UUID, agents.Agent, string) (int, bool, error) {
	return 0, false, errors.New("connection refused by 10.0.0.7:5432")
}

func TestAnAgentThatCantBeSavedIsUnavailableWithoutTheReason(t *testing.T) {
	f := newFixture(t, savingFails{fake.New()})
	rec := f.putFile(t, f.keyWith(t, access.PublishAgents), keeperFile("", "x\n"))
	if rec.Code != nethttp.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("%d %s; want 503 without the reason", rec.Code, rec.Body)
	}
}
