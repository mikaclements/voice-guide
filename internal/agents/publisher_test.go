package agents_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/agents/store/fake"
)

// voiceList is a fake of the text-to-speech service's voices: it answers names, or err, or waits
// for its caller to give up, as a service that stalls does.
type voiceList struct {
	names []string
	err   error
	block bool
	asked atomic.Int32
}

func (l *voiceList) Voices(ctx context.Context) ([]string, error) {
	l.asked.Add(1)
	if l.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return slices.Clone(l.names), l.err
}

var contextKinds = []string{"profile", "session_summary"}

func speaking(voice string) agents.Agent {
	a := lighthouseKeeper()
	a.Voice = voice
	return a
}

// The checks the script and the route share, each refusing before anything is saved.
func TestAPublisherRefusesWhatTheChecksRefuseAndSavesNothing(t *testing.T) {
	badSchema := lighthouseKeeper()
	badSchema.Summary, badSchema.SummarySchema = "Note what was said.", json.RawMessage(`{"type":"string"}`)
	noPersona := lighthouseKeeper()
	noPersona.Persona = ""
	diary := lighthouseKeeper()
	diary.Reads = []string{"profile", "diary"}
	nulPersona := lighthouseKeeper()
	nulPersona.Persona = "You answer\x00 every call."
	nulSchema := lighthouseKeeper()
	nulSchema.Summary, nulSchema.SummarySchema = "Note what was said.", json.RawMessage(`{"type":"object","description":"a\u0000b"}`)
	tests := map[string]struct {
		def  agents.Agent
		want string
		is   error
	}{
		"an agent the rules refuse":       {noPersona, "no persona", nil},
		"a summary schema that isn't one": {badSchema, "type: object", nil},
		"a kind user context lacks":       {diary, `reads "diary"`, nil},
		"a NUL in the persona":            {nulPersona, "NUL", nil},
		"a NUL in the schema":             {nulSchema, "NUL", nil},
		"a voice the service lacks":       {speaking("bf_emma"), "bf_emma", agents.ErrUnknownVoice},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			store := fake.New()
			p := agents.NewPublisher(store, contextKinds, &voiceList{names: []string{"af_heart"}})
			_, err := p.Publish(context.Background(), workspace, []agents.Agent{tt.def})
			var refusal agents.Refusal
			if !errors.As(err, &refusal) || !strings.Contains(err.Error(), tt.want) || (tt.is != nil && !errors.Is(err, tt.is)) {
				t.Errorf("Publish = %v; want a refusal with %q", err, tt.want)
			}
			if got, _ := store.Newest(context.Background(), workspace); len(got) != 0 {
				t.Errorf("a refused agent saved %d versions", len(got))
			}
		})
	}
}

func TestAPublisherSavesACheckedAgentAndAnUnchangedOneAddsNoVersion(t *testing.T) {
	p := agents.NewPublisher(fake.New(), contextKinds, &voiceList{names: []string{"bf_emma"}})
	for i, wantNew := range []bool{true, false} {
		res, err := p.Publish(context.Background(), workspace, []agents.Agent{speaking("bf_emma")})
		if err != nil || res.VoicesNotChecked != nil || len(res.Published) != 1 || res.Published[0].Number != 1 || res.Published[0].New != wantNew {
			t.Errorf("publish %d = %+v, %v; want version 1, new %v, voices checked", i+1, res, err, wantNew)
		}
	}
}

func TestAPublisherAsksForVoicesOnlyWhenAnAgentNamesOne(t *testing.T) {
	list := &voiceList{names: []string{"af_heart"}}
	res, err := agents.NewPublisher(fake.New(), contextKinds, list).Publish(context.Background(), workspace, []agents.Agent{lighthouseKeeper()})
	if err != nil || res.VoicesNotChecked != nil || list.asked.Load() != 0 {
		t.Errorf("Publish = %+v, %v with the list asked %d times; want it published, never asked", res, err, list.asked.Load())
	}
}

func TestAPublisherPublishesWhenTheServiceCantBeAskedAndSaysTheVoicesWereNotChecked(t *testing.T) {
	down := &voiceList{err: errors.New("connection refused")}
	for name, voices := range map[string]agents.VoiceList{"it is down": down, "there is none": nil} {
		t.Run(name, func(t *testing.T) {
			res, err := agents.NewPublisher(fake.New(), contextKinds, voices).Publish(context.Background(), workspace, []agents.Agent{speaking("bf_emma")})
			if err != nil || !errors.Is(res.VoicesNotChecked, agents.ErrVoicesNotChecked) || len(res.Published) != 1 {
				t.Errorf("Publish = %+v, %v; want it published, voices not checked", res, err)
			}
		})
	}
}

// A service that stalls is given VoicesTimeout, then the agent is published unchecked. The clock
// is the test's, so the wait costs nothing.
func TestAPublisherGivesUpOnAStalledServiceAfterTheVoicesTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		res, err := agents.NewPublisher(fake.New(), contextKinds, &voiceList{block: true}).Publish(context.Background(), workspace, []agents.Agent{speaking("bf_emma")})
		if err != nil || res.VoicesNotChecked == nil || len(res.Published) != 1 || time.Since(start) != agents.VoicesTimeout {
			t.Errorf("Publish = %+v, %v after %v; want it published unchecked after %v", res, err, time.Since(start), agents.VoicesTimeout)
		}
	})
}

func TestAPublisherReportsAStoreThatFailsAsNoRefusal(t *testing.T) {
	_, err := agents.NewPublisher(failingStore{fake.New()}, contextKinds, nil).Publish(context.Background(), workspace, []agents.Agent{lighthouseKeeper()})
	var refusal agents.Refusal
	if err == nil || errors.As(err, &refusal) {
		t.Errorf("Publish = %v; want the store's error, not a refusal", err)
	}
}

type failingStore struct{ agents.Versions }

func (failingStore) Save(context.Context, uuid.UUID, agents.Agent, string) (int, bool, error) {
	return 0, false, errors.New("connection refused")
}
