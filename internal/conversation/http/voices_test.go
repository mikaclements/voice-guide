package http_test

import (
	nethttp "net/http"
	"testing"
	"uuid"
)

func startWith(extra string) string {
	return `{"agent_id":"` + keeperID.String() + `","request_id":"` + uuid.NewV7().String() + `","user":"aiko-42",` + extra + `}`
}

func TestAStartNamingAVoiceAndSpeedAnswers201(t *testing.T) {
	f := newFixture(t)
	if rec := f.send("POST", "/v1/sessions", f.productKey(t), startWith(`"voice":"bf_emma","speed":1.25`)); rec.Code != nethttp.StatusCreated {
		t.Errorf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
}

func TestAVoiceOutOfFormOrASpeedOutOfRangeIsA422(t *testing.T) {
	f := newFixture(t)
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
