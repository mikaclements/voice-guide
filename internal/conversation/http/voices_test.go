package http_test

import (
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
	voicefake "github.com/mikaclements/voice-guide/internal/conversation/voicelist/fake"
	"github.com/mikaclements/voice-guide/internal/productkey"
	keysfake "github.com/mikaclements/voice-guide/internal/productkey/store/fake"
	"github.com/mikaclements/voice-guide/internal/ticket"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	itemsfake "github.com/mikaclements/voice-guide/internal/usercontext/store/fake"
)

// fixtureWithVoices returns the API over a service that checks voices against list.
func fixtureWithVoices(t *testing.T, list *voicefake.List) fixture {
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
	f := fixtureWithVoices(t, voicefake.New("af_heart", "bf_emma"))
	rec := f.do("GET", "/v1/voices", f.productKey(t))
	got := decode[struct {
		Voices []string `json:"voices"`
	}](t, rec)
	if rec.Code != nethttp.StatusOK || !slices.Equal(got.Voices, []string{"af_heart", "bf_emma"}) {
		t.Errorf("GET /v1/voices = %d %s, want 200 with af_heart and bf_emma", rec.Code, rec.Body)
	}
}

func TestTheVoicesRouteNeedsAProductKey(t *testing.T) {
	f := fixtureWithVoices(t, voicefake.New("af_heart"))
	assertProblem(t, f.do("GET", "/v1/voices", ""), nethttp.StatusUnauthorized)
}

func TestTheVoicesRouteAnswers503WithoutTheReasonWhileTheServiceIsDown(t *testing.T) {
	list := voicefake.New()
	list.Fail(errors.New("dial 10.0.0.7: refused"))
	f := fixtureWithVoices(t, list)
	rec := f.do("GET", "/v1/voices", f.productKey(t))
	assertProblem(t, rec, nethttp.StatusServiceUnavailable)
	if strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("body %s gives away the reason", rec.Body)
	}
}

func TestAStartNamingAVoiceTheServiceOffersAnswers201WithItsVoiceAndSpeed(t *testing.T) {
	f := fixtureWithVoices(t, voicefake.New("af_heart", "bf_emma"))
	rec := f.send("POST", "/v1/sessions", f.productKey(t), startWith(`"voice":"bf_emma","speed":1.25`))
	got := decode[struct {
		Voice string  `json:"voice"`
		Speed float64 `json:"speed"`
	}](t, rec)
	if rec.Code != nethttp.StatusCreated || got.Voice != "bf_emma" || got.Speed != 1.25 {
		t.Errorf("status %d with %+v, want 201 in bf_emma at 1.25: %s", rec.Code, got, rec.Body)
	}
}

func TestAStartNamingNoVoiceAnswersWithTheServersVoiceAndSpeed(t *testing.T) {
	f := fixtureWithVoices(t, voicefake.New("af_heart"))
	got := decode[struct {
		Voice string  `json:"voice"`
		Speed float64 `json:"speed"`
	}](t, f.send("POST", "/v1/sessions", f.productKey(t), startWith(`"session_context":[]`)))
	if got.Voice != "af_heart" || got.Speed != 1 {
		t.Errorf("reply voice %+v, want the server's af_heart at 1", got)
	}
}

func TestARepeatNamingAnotherVoiceGetsTheFirstStartsInTheReply(t *testing.T) {
	f := fixtureWithVoices(t, voicefake.New("af_heart", "bf_emma"))
	key, request := f.productKey(t), uuid.NewV7().String()
	body := func(voice string) string {
		return `{"agent_id":"` + keeperID.String() + `","request_id":"` + request + `","user":"aiko-42","voice":"` + voice + `"}`
	}
	f.send("POST", "/v1/sessions", key, body("bf_emma"))
	rec := f.send("POST", "/v1/sessions", key, body("af_heart"))
	got := decode[struct {
		Voice string `json:"voice"`
	}](t, rec)
	if rec.Code != nethttp.StatusOK || got.Voice != "bf_emma" {
		t.Errorf("the repeat = %d in %q, want 200 with the first start's bf_emma", rec.Code, got.Voice)
	}
}

func TestAStartNamingAnUnknownVoiceIsA422OfItsOwnType(t *testing.T) {
	f := fixtureWithVoices(t, voicefake.New("af_heart"))
	rec := f.send("POST", "/v1/sessions", f.productKey(t), startWith(`"voice":"bf_emma"`))
	assertProblemType(t, rec, nethttp.StatusUnprocessableEntity, "/v1/problems/unknown-voice")
}

func TestAVoiceOutOfFormOrASpeedOutOfRangeIsA422(t *testing.T) {
	f := fixtureWithVoices(t, voicefake.New("af_heart"))
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
	list := voicefake.New()
	list.Fail(errors.New("dial 10.0.0.7: refused"))
	f := fixtureWithVoices(t, list)
	rec := f.send("POST", "/v1/sessions", f.productKey(t), startWith(`"voice":"af_heart"`))
	assertProblem(t, rec, nethttp.StatusServiceUnavailable)
	if strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("body %s gives away the reason", rec.Body)
	}
}

func TestAStartNamingNoVoiceStartsWhileTheListIsDown(t *testing.T) {
	list := voicefake.New()
	list.Fail(errors.New("down"))
	f := fixtureWithVoices(t, list)
	if rec := f.send("POST", "/v1/sessions", f.productKey(t), startWith(`"speed":1.5`)); rec.Code != nethttp.StatusCreated || list.Asked() != 0 {
		t.Errorf("a start naming no voice = %d with the list asked %d times, want 201 and none: %s", rec.Code, list.Asked(), rec.Body)
	}
}
