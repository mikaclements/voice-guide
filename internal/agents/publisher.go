package agents

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
	"uuid"
)

// VoiceList is the voices the text-to-speech service offers, which the voice
// check asks. It is the agents module's own port, so agents imports no module:
// whoever wires a Publisher hands in the service's list.
type VoiceList interface {
	// Voices returns the names of the voices that can speak a reply.
	Voices(ctx context.Context) ([]string, error)
}

// VoicesTimeout bounds asking the text-to-speech service for its voices, which
// is a few hundred bytes from a service on this network, so 2s is a wide margin
// without holding up a publish when it isn't running. A round guess, to be
// measured the first time a publish reports that Kokoro was slow rather than
// down.
const VoicesTimeout = 2 * time.Second

var (
	// ErrUnknownVoice reports an agent naming a voice the text-to-speech
	// service doesn't offer.
	ErrUnknownVoice = errors.New("not a voice the text-to-speech service offers")
	// ErrVoicesNotChecked reports that the voices couldn't be checked, as the
	// text-to-speech service couldn't be asked.
	ErrVoicesNotChecked = errors.New("the text-to-speech service couldn't be asked for its voices")
)

// unknownVoice is the error CheckVoices returns: its message is the one the script has always
// shown, and errors.Is finds ErrUnknownVoice in it.
type unknownVoice struct{ msg string }

func (e unknownVoice) Error() string        { return e.msg }
func (e unknownVoice) Is(target error) bool { return target == ErrUnknownVoice }

// Refusal is an agent file a check turned down, as opposed to a store that
// failed. Its message is safe to show whoever wrote the file.
type Refusal struct{ Err error }

func (r Refusal) Error() string { return r.Err.Error() }
func (r Refusal) Unwrap() error { return r.Err }

// Publisher publishes agents after every check, the one way in whether a
// workspace publishes from a folder or through the API, so the two can't check
// differently.
type Publisher struct {
	store  Versions
	kinds  []string
	voices VoiceList
}

// NewPublisher returns a Publisher saving to store. kinds are the kinds of
// context user context holds, which an agent may read, and voices the
// text-to-speech service's list; nil, no voice can be checked.
func NewPublisher(store Versions, kinds []string, voices VoiceList) *Publisher {
	return &Publisher{store: store, kinds: slices.Clone(kinds), voices: voices}
}

// Result is what a checked publish did.
type Result struct {
	Published []Published
	// VoicesNotChecked is why an agent's voice wasn't checked, wrapping
	// ErrVoicesNotChecked; nil when it was, or no agent names one. The agents
	// were published all the same: the service needn't be running to publish.
	VoicesNotChecked error
}

// Publish checks defs, then saves them as Publish does: the agents' own rules,
// the context kinds user context holds, then the voices. A refused file is a
// Refusal; a store that fails is not.
func (p *Publisher) Publish(ctx context.Context, workspace uuid.UUID, defs []Agent) (Result, error) {
	if err := Check(defs); err != nil {
		return Result{}, Refusal{err}
	}
	if err := CheckReads(defs, p.kinds); err != nil {
		return Result{}, Refusal{err}
	}
	var res Result
	if slices.ContainsFunc(defs, func(a Agent) bool { return a.Voice != "" }) {
		switch err := p.checkVoices(ctx, defs); {
		case errors.Is(err, ErrVoicesNotChecked):
			res.VoicesNotChecked = err
		case err != nil:
			return Result{}, Refusal{err}
		}
	}
	var err error
	res.Published, err = Publish(ctx, p.store, workspace, defs)
	return res, err
}

// checkVoices asks the service for its voices, giving up after VoicesTimeout.
func (p *Publisher) checkVoices(ctx context.Context, defs []Agent) error {
	if p.voices == nil {
		return fmt.Errorf("%w: there is none to ask", ErrVoicesNotChecked)
	}
	ctx, cancel := context.WithTimeout(ctx, VoicesTimeout)
	defer cancel()
	offered, err := p.voices.Voices(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrVoicesNotChecked, err)
	}
	return CheckVoices(defs, offered)
}
