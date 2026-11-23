package conversation

import (
	"context"
	"time"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/usercontext"
)

// Service holds conversation's use cases. The inbound adapters call it once they
// have proven who the caller is.
type Service struct {
	catalog     *agents.Catalog
	sessions    Sessions
	turns       Turns
	model       Model
	voices      VoiceList
	userContext *usercontext.Service
	settings    Settings
	// now is the clock a turn's stages are timed by: the system's, or a
	// test's, so stage times are checked exactly.
	now func() time.Time
}

// Settings bound what sessions and turns take in, in characters, how long a
// turn runs and how long a session stays idle, and say which model answers.
type Settings struct {
	// UserContextChars bounds what is loaded from user context.
	UserContextChars int
	// SessionContextChars bounds the session context a product sends.
	SessionContextChars int
	// CarriedChars bounds the words and replies of the sessions a session
	// carries that its prompt holds, the newest kept.
	CarriedChars int
	// ScreenInputChars bounds the screen input sent with a turn.
	ScreenInputChars int
	// WordsChars bounds the words the user sends in a turn.
	WordsChars int
	// TurnDeadline is the longest a turn runs: one in flight for longer was
	// cut off, by a crash say, and no longer holds up its session.
	TurnDeadline time.Duration
	// Model names the model that answers turns of an agent version that
	// names none of its own. It is sent with every request.
	Model string
	// SessionIdle is how long a session goes with no turn started and no
	// speech heard before it ends.
	SessionIdle time.Duration
	// Voice is the voice replies are spoken in for a session whose start and
	// agent version name none, as text-to-speech names it.
	Voice string
	// Speed is how fast that voice speaks, 1 its own pace, when neither names one.
	Speed float64
	// VoicesTimeout is how long the voice list is waited for; a default when zero.
	VoicesTimeout time.Duration
}

// NewService returns the service over the agents the catalog offers, the
// stores of sessions and their turns, the model that answers turns, and user
// context, which holds what voice-guide has learned about each user.
func NewService(catalog *agents.Catalog, sessions Sessions, turns Turns, model Model, userContext *usercontext.Service, settings Settings, opts ...Option) *Service {
	s := &Service{catalog: catalog, sessions: sessions, turns: turns, model: model, userContext: userContext, settings: settings, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// modelOf is the model that answers for an agent version: its own when its
// file names one, else the server's.
func (s *Service) modelOf(v agents.Version) string {
	if v.Model != "" {
		return v.Model
	}
	return s.settings.Model
}

// SessionContextLimit is how many characters of session context a start may carry.
func (s *Service) SessionContextLimit() int { return s.settings.SessionContextChars }

// TurnLimit is how many characters of words and screen input a turn may carry.
func (s *Service) TurnLimit() int { return s.settings.WordsChars + s.settings.ScreenInputChars }

// SessionIdle is how long a session goes with no turn started and no speech
// heard before it ends.
func (s *Service) SessionIdle() time.Duration { return s.settings.SessionIdle }

// TurnDeadline is the longest a turn runs.
func (s *Service) TurnDeadline() time.Duration { return s.settings.TurnDeadline }

// Agents lists the agents a product's server may start a session with: the
// newest version of each of its workspace's agents.
func (s *Service) Agents(ctx context.Context, c access.Caller) ([]agents.Version, error) {
	return s.catalog.Available(ctx, c.Workspace)
}
