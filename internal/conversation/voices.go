package conversation

import (
	"cmp"
	"errors"
	"fmt"

	"github.com/mikaclements/voice-guide/internal/agents"
)

// ErrVoiceSettings reports a start naming a voice that isn't shaped like a
// voice's name, or a speed outside the range a voice may speak at.
var ErrVoiceSettings = errors.New("bad voice or speed")

// checkVoiceSettings refuses a voice named at a start whose form isn't a
// voice's name, and a speed outside the bounds, before either reaches a
// request to the text-to-speech service.
func checkVoiceSettings(r StartRequest) error {
	if r.Voice != "" && !agents.IsVoiceName(r.Voice) {
		return fmt.Errorf("%w: voice must be a language and a style, such as af_heart: two lower-case letters, an underscore, then lower-case letters and digits", ErrVoiceSettings)
	}
	if r.Speed != nil && !agents.IsSpeed(*r.Speed) {
		return fmt.Errorf("%w: speed must be from %v to %v", ErrVoiceSettings, agents.MinSpeed, agents.MaxSpeed)
	}
	return nil
}

// voiceOf is the voice and speed a session speaks in: the start's, else the
// agent version's, else the server's, each of the two chosen on its own.
func (s *Service) voiceOf(r StartRequest, v agents.Version) (string, float64) {
	voice := cmp.Or(r.Voice, v.Voice, s.settings.Voice)
	speed := cmp.Or(v.Speed, s.settings.Speed)
	if r.Speed != nil {
		speed = *r.Speed
	}
	return voice, speed
}

// Speaking returns the voice and speed to speak a session's replies in: the
// ones it started with, or the server's for a session started before sessions
// kept them.
func (s *Service) Speaking(session Session) (voice string, speed float64) {
	return cmp.Or(session.Voice, s.settings.Voice), cmp.Or(session.Speed, s.settings.Speed)
}
