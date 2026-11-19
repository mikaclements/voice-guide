package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/mikaclements/voice-guide/internal/agents"
	agentsfake "github.com/mikaclements/voice-guide/internal/agents/store/fake"
	"github.com/mikaclements/voice-guide/internal/api"
	conversationhttp "github.com/mikaclements/voice-guide/internal/conversation/http"
	conversationfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/productkey"
	keysfake "github.com/mikaclements/voice-guide/internal/productkey/store/fake"
	"github.com/mikaclements/voice-guide/internal/ticket"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	itemsfake "github.com/mikaclements/voice-guide/internal/usercontext/store/fake"
)

// The service as run builds conversation through newConversation with the text-to-speech's voice
// list; without that wiring every start naming a voice would be refused, and compiling can't tell.
func TestTheVoicesRouteAnswers200WithTheTextToSpeechServicesVoices(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"VOICE_TTS": "fake"}))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	_, voices := newTextToSpeech(cfg)
	svc := newConversation(agents.NewCatalog(agentsfake.New()), conversationfake.New(), conversationfake.NewTurns(), nil, usercontext.NewService(itemsfake.New()), cfg, voices)
	key, err := ticket.NewKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	keys := productkey.NewKeys(keysfake.New())
	secret, _, err := keys.Create(context.Background(), cfg.workspace)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rt := api.NewRouter(key, keys, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	conversationhttp.Register(rt, svc)

	req := httptest.NewRequest("GET", "/v1/voices", nil)
	req.Header.Set("Authorization", "Bearer "+secret.Reveal())
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	var got struct {
		Voices []string `json:"voices"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil || rec.Code != http.StatusOK || !slices.Contains(got.Voices, "af_heart") {
		t.Errorf("GET /v1/voices = %d %v (%v), want 200 listing af_heart", rec.Code, got.Voices, err)
	}
}
