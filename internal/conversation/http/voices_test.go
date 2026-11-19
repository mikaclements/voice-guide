package http_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	nethttp "net/http"
	"slices"
	"strings"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/conversation"
	sessionsfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/productkey"
	keysfake "github.com/mikaclements/voice-guide/internal/productkey/store/fake"
	"github.com/mikaclements/voice-guide/internal/ticket"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	itemsfake "github.com/mikaclements/voice-guide/internal/usercontext/store/fake"
)

// voiceList is the text-to-speech service's list in these tests: the fake of conversation's voice
// port.
type voiceList struct {
	names []string
	err   error
}

func (v voiceList) Voices(context.Context) ([]string, error) { return v.names, v.err }

// fixtureWithVoices returns the API over a service that checks voices against list.
func fixtureWithVoices(t *testing.T, list voiceList) fixture {
	t.Helper()
	key, err := ticket.NewKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	keys := productkey.NewKeys(keysfake.New())
	svc := conversation.NewService(agents.NewCatalog(newFixtureStore(t)), sessionsfake.New(), nil, nil, usercontext.NewService(itemsfake.New()),
		conversation.Settings{UserContextChars: 1000, SessionContextChars: 1000, Voice: "af_heart", Speed: 1}, conversation.WithVoices(list))
	return fixture{handler: handlerFor(svc, key, keys, slog.New(slog.NewTextHandler(io.Discard, nil))), key: key, keys: keys}
}

func startWith(extra string) string {
	return `{"agent_id":"` + keeperID.String() + `","request_id":"` + uuid.NewV7().String() + `","user":"aiko-42",` + extra + `}`
}

func TestTheVoicesRouteListsTheTextToSpeechServicesVoices(t *testing.T) {
	f := fixtureWithVoices(t, voiceList{names: []string{"af_heart", "bf_emma"}})
	rec := f.do("GET", "/v1/voices", f.productKey(t))
	got := decode[struct {
		Voices []string `json:"voices"`
	}](t, rec)
	if rec.Code != nethttp.StatusOK || !slices.Equal(got.Voices, []string{"af_heart", "bf_emma"}) {
		t.Errorf("GET /v1/voices = %d %s, want 200 with af_heart and bf_emma", rec.Code, rec.Body)
	}
}

func TestTheVoicesRouteNeedsAProductKeyAndAnswers503WithoutTheReasonWhileTheServiceIsDown(t *testing.T) {
	f := fixtureWithVoices(t, voiceList{err: errors.New("dial 10.0.0.7: refused")})
	assertProblem(t, f.do("GET", "/v1/voices", ""), nethttp.StatusUnauthorized)
	rec := f.do("GET", "/v1/voices", f.productKey(t))
	assertProblem(t, rec, nethttp.StatusServiceUnavailable)
	if strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("body %s gives away the reason", rec.Body)
	}
}

func TestAStartNamingAVoiceTheServiceOffersAnswers201(t *testing.T) {
	f := fixtureWithVoices(t, voiceList{names: []string{"af_heart", "bf_emma"}})
	if rec := f.send("POST", "/v1/sessions", f.productKey(t), startWith(`"voice":"bf_emma","speed":1.25`)); rec.Code != nethttp.StatusCreated {
		t.Errorf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
}

func TestAStartNamingAnUnknownVoiceIsA422OfItsOwnType(t *testing.T) {
	f := fixtureWithVoices(t, voiceList{names: []string{"af_heart"}})
	rec := f.send("POST", "/v1/sessions", f.productKey(t), startWith(`"voice":"bf_emma"`))
	assertProblemType(t, rec, nethttp.StatusUnprocessableEntity, "/v1/problems/unknown-voice")
}

func TestAVoiceOutOfFormOrASpeedOutOfRangeIsA422(t *testing.T) {
	f := fixtureWithVoices(t, voiceList{names: []string{"af_heart"}})
	for name, extra := range map[string]string{
		"a path as the voice": `"voice":"../af_heart"`,
		"too slow":            `"speed":0.4`,
		"too fast":            `"speed":2.1`,
		"a speed of nothing":  `"speed":0`,
	} {
		t.Run(name, func(t *testing.T) {
			assertProblemType(t, f.send("POST", "/v1/sessions", f.productKey(t), startWith(extra)), nethttp.StatusUnprocessableEntity, "/v1/problems/voice-settings")
		})
	}
}

func TestAStartNamingAVoiceWhileTheListIsUnreachableIsA503WithoutTheReason(t *testing.T) {
	f := fixtureWithVoices(t, voiceList{err: errors.New("dial 10.0.0.7: refused")})
	rec := f.send("POST", "/v1/sessions", f.productKey(t), startWith(`"voice":"af_heart"`))
	assertProblem(t, rec, nethttp.StatusServiceUnavailable)
	if strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("body %s gives away the reason", rec.Body)
	}
	// A start naming none doesn't ask, so it starts with the list down.
	if rec := f.send("POST", "/v1/sessions", f.productKey(t), startWith(`"speed":1.5`)); rec.Code != nethttp.StatusCreated {
		t.Errorf("a start naming no voice = %d, want 201: %s", rec.Code, rec.Body)
	}
}
