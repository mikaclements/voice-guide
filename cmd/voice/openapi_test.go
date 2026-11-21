package main

import (
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/mikaclements/voice-guide/internal/agents"
	agentsfake "github.com/mikaclements/voice-guide/internal/agents/store/fake"
	"github.com/mikaclements/voice-guide/internal/api"
	conversationfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/speech"
	detectionfake "github.com/mikaclements/voice-guide/internal/speech/detection/fake"
	transportfake "github.com/mikaclements/voice-guide/internal/speech/transport/fake"
	"github.com/mikaclements/voice-guide/internal/ticket"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	itemsfake "github.com/mikaclements/voice-guide/internal/usercontext/store/fake"
)

// servedRoutes wires the router as run does, over fakes, and returns what it serves.
func servedRoutes(t *testing.T) []string {
	t.Helper()
	cfg, err := loadConfig(env(map[string]string{"VOICE_TTS": "fake"}))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	tts, voices := newTextToSpeech(cfg)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	catalog := agents.NewCatalog(agentsfake.New())
	svc := newConversation(catalog, conversationfake.New(), conversationfake.NewTurns(), nil, usercontext.NewService(itemsfake.New()), cfg, voices)
	audio := speech.NewService(svc, transportfake.New(), tts, detectionfake.Detection{}, nil, nil, speech.Settings{}, log)
	key, err := ticket.NewKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	routes := api.NewRouter(key, nil, time.Now, log)
	registerRoutes(routes, svc, audio, catalog, agents.NewPublisher(agentsfake.New(), usercontext.Kinds(), nil), t.TempDir())
	return routes.Routes()
}

func readSpec(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("read the API description: %v", err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse the API description: %v", err)
	}
	return spec
}

// operations lists the file's operations as the router names its routes: "GET /v1/agents".
func operations(spec map[string]any) []string {
	var ops []string
	paths, _ := spec["paths"].(map[string]any)
	for path, item := range paths {
		methods, _ := item.(map[string]any)
		for method := range methods {
			// Keys of a path item that aren't operations, such as the parameters shared by all of them.
			if slices.Contains([]string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}, method) {
				ops = append(ops, strings.ToUpper(method)+" "+path)
			}
		}
	}
	slices.Sort(ops)
	return ops
}

// The description is what products generate their client types from, so a route it lacks, or
// one it describes that the service doesn't serve, misleads them.
func TestTheAPIDescriptionHasEveryRouteTheServiceServesAndOnlyThose(t *testing.T) {
	served := servedRoutes(t)
	slices.Sort(served)
	described := operations(readSpec(t))
	for _, r := range served {
		if !slices.Contains(described, r) {
			t.Errorf("api/openapi.yaml has no operation for the route %q the service serves", r)
		}
	}
	for _, d := range described {
		if !slices.Contains(served, d) {
			t.Errorf("api/openapi.yaml describes %q, which the service doesn't serve", d)
		}
	}
}

// refs collects every $ref in the document.
func refs(node any, out *[]string) {
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			if s, ok := v.(string); ok && k == "$ref" {
				*out = append(*out, s)
				continue
			}
			refs(v, out)
		}
	case []any:
		for _, v := range n {
			refs(v, out)
		}
	}
}

func TestEveryReferenceInTheAPIDescriptionResolves(t *testing.T) {
	spec := readSpec(t)
	var found []string
	refs(spec, &found)
	if len(found) == 0 {
		t.Fatal("no $ref found in the description; the check reads nothing")
	}
	for _, ref := range found {
		path, ok := strings.CutPrefix(ref, "#/")
		if !ok {
			t.Errorf("$ref %q points outside the file", ref)
			continue
		}
		var at any = spec
		for _, part := range strings.Split(path, "/") {
			m, _ := at.(map[string]any)
			if at = m[part]; at == nil {
				t.Errorf("$ref %q resolves to nothing", ref)
				break
			}
		}
	}
}
