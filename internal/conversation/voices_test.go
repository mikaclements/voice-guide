package conversation_test

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/usercontext"
)

// offeredVoices is the text-to-speech service's list in these tests: the fake of conversation's
// voice port. It counts the times it was asked, and fails them while err is set.
type offeredVoices struct {
	names []string
	err   error
	asked int
}

func (o *offeredVoices) Voices(context.Context) ([]string, error) {
	o.asked++
	return slices.Clone(o.names), o.err
}

// withVoices returns the harness with a service that checks voices against the list, and
// speaks in the server's af_heart at its own pace unless the agent or the start says otherwise.
func withVoices(t *testing.T) (serviceOverFakes, *offeredVoices) {
	t.Helper()
	h := newServiceOverFakes(t)
	list := &offeredVoices{names: []string{"af_heart", "bf_emma", "am_adam"}}
	h.settings.Voice, h.settings.Speed = "af_heart", 1
	h.svc = conversation.NewService(agents.NewCatalog(h.agents), h.sessions, h.turns, h.model, usercontext.NewService(h.items), h.settings, conversation.WithVoices(list))
	return h, list
}

func speed(f float64) *float64 { return &f }

func TestASessionSpeaksInTheServersVoiceUnlessTheAgentOrTheStartNamesOne(t *testing.T) {
	h, list := withVoices(t)
	h.publish(t, agents.Agent{ID: captainID, Name: "Ferry captain", Persona: "You know every crossing by heart.", Voice: "bf_emma", Speed: 1.25})

	server, _ := h.start(t, caller, keeperID, uuid.NewV7())
	agent, _ := h.start(t, caller, captainID, uuid.NewV7())
	started, _, err := h.startAs(caller, conversation.StartRequest{Agent: captainID, RequestID: uuid.NewV7(), Voice: "am_adam", Speed: speed(0.5)})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	for name, tt := range map[string]struct {
		got   conversation.Session
		voice string
		speed float64
	}{
		"the server's, when neither names one":      {server, "af_heart", 1},
		"the agent's, when the start names none":    {agent, "bf_emma", 1.25},
		"the start's over the agent's, each of two": {started, "am_adam", 0.5},
	} {
		if tt.got.Voice != tt.voice || tt.got.Speed != tt.speed {
			t.Errorf("%s: session speaks in %q at %v, want %q at %v", name, tt.got.Voice, tt.got.Speed, tt.voice, tt.speed)
		}
		if v, s := h.svc.Speaking(tt.got); v != tt.voice || s != tt.speed {
			t.Errorf("%s: Speaking = %q at %v, want %q at %v", name, v, s, tt.voice, tt.speed)
		}
	}
	// The start's voice only: the agent's speed still applies.
	voiceOnly, _, err := h.startAs(caller, conversation.StartRequest{Agent: captainID, RequestID: uuid.NewV7(), Voice: "af_heart"})
	if err != nil || voiceOnly.Voice != "af_heart" || voiceOnly.Speed != 1.25 {
		t.Errorf("a start naming a voice only = %q at %v (%v), want af_heart at the agent's 1.25", voiceOnly.Voice, voiceOnly.Speed, err)
	}
	if list.asked != 2 {
		t.Errorf("the voices were asked %d times, want 2: once for each start that named a voice, none for the others", list.asked)
	}
}

func TestASessionStartedBeforeSessionsKeptTheirVoiceSpeaksInTheServers(t *testing.T) {
	h, _ := withVoices(t)
	if v, s := h.svc.Speaking(conversation.Session{}); v != "af_heart" || s != 1 {
		t.Errorf("Speaking = %q at %v, want the server's af_heart at 1", v, s)
	}
}

func TestAStartNamingAVoiceTheServiceDoesNotOfferIsRefusedAndNothingSaved(t *testing.T) {
	h, _ := withVoices(t)
	_, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), Voice: "zf_nobody"})
	if !errors.Is(err, conversation.ErrUnknownVoice) || h.sessions.Count() != 0 {
		t.Errorf("StartSession = %v with %d sessions, want ErrUnknownVoice and none", err, h.sessions.Count())
	}
}

func TestAVoiceOrSpeedOutOfFormOrRangeIsRefusedBeforeAnyRequestToTheService(t *testing.T) {
	for name, r := range map[string]conversation.StartRequest{
		"a path":                {Voice: "../../etc/passwd"},
		"capitals":              {Voice: "AF_Heart"},
		"a mix of voices":       {Voice: "af_heart+bf_emma"},
		"no style":              {Voice: "af_"},
		"a speed just under":    {Speed: speed(0.49)},
		"a speed just over":     {Speed: speed(2.01)},
		"a speed of nothing":    {Speed: speed(0)},
		"a negative speed":      {Speed: speed(-1)},
		"a speed that's no one": {Speed: speed(nan())},
	} {
		t.Run(name, func(t *testing.T) {
			h, list := withVoices(t)
			r.Agent, r.RequestID = keeperID, uuid.NewV7()
			_, _, err := h.startAs(caller, r)
			if !errors.Is(err, conversation.ErrVoiceSettings) || list.asked != 0 || h.sessions.Count() != 0 {
				t.Errorf("StartSession = %v, asked %d times, %d sessions; want ErrVoiceSettings, none asked, none saved", err, list.asked, h.sessions.Count())
			}
		})
	}
}

func TestASpeedAtEitherEndOfTheRangeIsAccepted(t *testing.T) {
	for _, s := range []float64{agents.MinSpeed, agents.MaxSpeed} {
		h, _ := withVoices(t)
		got, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), Speed: speed(s)})
		if err != nil || got.Speed != s {
			t.Errorf("a start at speed %v = %v at %v, want it accepted", s, err, got.Speed)
		}
	}
}

func TestAStartNamingNoVoiceNeverAsksSoATextOnlySessionDoesNotDependOnTheService(t *testing.T) {
	h, list := withVoices(t)
	list.err = errors.New("text-to-speech is down")
	if _, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7()}); err != nil || list.asked != 0 {
		t.Errorf("StartSession = %v, voices asked %d times; want a start without asking", err, list.asked)
	}
}

func TestAStartNamingAVoiceWhileTheListIsUnreachableIsRefusedAndTheSameRequestCanBeTriedAgain(t *testing.T) {
	h, list := withVoices(t)
	list.err = errors.New("text-to-speech is down")
	r := conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), Voice: "bf_emma"}
	if _, _, err := h.startAs(caller, r); !errors.Is(err, conversation.ErrVoicesUnavailable) || h.sessions.Count() != 0 {
		t.Fatalf("StartSession = %v with %d sessions, want ErrVoicesUnavailable and none", err, h.sessions.Count())
	}
	list.err = nil
	if got, isNew, err := h.startAs(caller, r); err != nil || !isNew || got.Voice != "bf_emma" {
		t.Errorf("the same request after it came back = %+v, %v, %v; want a new session in bf_emma", got, isNew, err)
	}
}

func TestAServiceWithNoVoiceListRefusesAStartNamingAVoice(t *testing.T) {
	h := newServiceOverFakes(t)
	if _, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: uuid.NewV7(), Voice: "af_heart"}); !errors.Is(err, conversation.ErrVoicesUnavailable) {
		t.Errorf("StartSession = %v, want ErrVoicesUnavailable", err)
	}
}

func TestARepeatedStartKeepsTheFirstStartsVoiceAndSpeed(t *testing.T) {
	h, _ := withVoices(t)
	request := uuid.NewV7()
	first, _, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: request, Voice: "bf_emma", Speed: speed(1.5)})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	again, isNew, err := h.startAs(caller, conversation.StartRequest{Agent: keeperID, RequestID: request, Voice: "am_adam", Speed: speed(0.7)})
	if err != nil || isNew || again.ID != first.ID || again.Voice != "bf_emma" || again.Speed != 1.5 {
		t.Errorf("the repeat = %+v, new %v, %v; want the first session, in bf_emma at 1.5", again, isNew, err)
	}
}

func TestTheVoicesAServiceListsAreThoseTheTextToSpeechServiceOffers(t *testing.T) {
	h, list := withVoices(t)
	got, err := h.svc.Voices(context.Background())
	if err != nil || !slices.Equal(got, list.names) {
		t.Errorf("Voices = %v, %v; want %v", got, err, list.names)
	}
	list.err = errors.New("down")
	if _, err := h.svc.Voices(context.Background()); !errors.Is(err, conversation.ErrVoicesUnavailable) {
		t.Errorf("Voices = %v while the list is down, want ErrVoicesUnavailable", err)
	}
}

func nan() float64 { return math.NaN() }
