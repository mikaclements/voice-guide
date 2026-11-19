package conversation

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/mikaclements/voice-guide/internal/agents"
)

// VoiceList is the voices the text-to-speech service offers. Conversation stays
// text-only: it holds a session's voice and speed as settings, and asks this
// only to check a voice a start names.
type VoiceList interface {
	// Voices returns the names of the voices that can speak a reply.
	Voices(ctx context.Context) ([]string, error)
}

// Option changes how NewService builds the service.
type Option func(*Service)

// WithVoices sets the list a voice named at a session's start is checked
// against. Without it, a start naming a voice can't be checked and is refused
// as ErrVoicesUnavailable, while a start naming none never asks.
func WithVoices(v VoiceList) Option {
	return func(s *Service) { s.voices = v }
}

// ErrVoiceSettings reports a start naming a voice that isn't shaped like a
// voice's name, or a speed outside the range a voice may speak at.
var ErrVoiceSettings = errors.New("bad voice or speed")

// ErrUnknownVoice reports a start naming a voice the text-to-speech service
// doesn't offer.
var ErrUnknownVoice = errors.New("no such voice")

// ErrVoicesUnavailable reports a start naming a voice when the voices on offer
// couldn't be read. Nothing was saved, so the same request can be retried.
var ErrVoicesUnavailable = errors.New("the voices couldn't be listed")

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

// checkVoiceOffered refuses a voice the text-to-speech service doesn't offer.
// A start naming none doesn't ask, so a text-only session never depends on it.
func (s *Service) checkVoiceOffered(ctx context.Context, voice string) error {
	if voice == "" {
		return nil
	}
	if s.voices == nil {
		return fmt.Errorf("%w: no text-to-speech service to ask", ErrVoicesUnavailable)
	}
	offered, err := s.voices.Voices(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrVoicesUnavailable, err)
	}
	if !slices.Contains(offered, voice) {
		return fmt.Errorf("%w: %q isn't one of the text-to-speech service's voices", ErrUnknownVoice, voice)
	}
	return nil
}

// Voices lists the voices a session may speak in, for a product's server to
// choose from.
func (s *Service) Voices(ctx context.Context) ([]string, error) {
	if s.voices == nil {
		return nil, fmt.Errorf("%w: no text-to-speech service to ask", ErrVoicesUnavailable)
	}
	offered, err := s.voices.Voices(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrVoicesUnavailable, err)
	}
	return offered, nil
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
