package speech

import (
	"context"
	"time"
)

// TurnAudio is how much of the user's latest audio the end-of-turn check reads: the 8 s Smart
// Turn was trained on.
const TurnAudio = 8 * time.Second

// EndOfTurn judges from the user's latest audio whether they have finished speaking.
type EndOfTurn interface {
	// Finished reports whether the user has finished, from the last TurnAudio of audio, 16 kHz
	// mono PCM samples; anything earlier is not read.
	Finished(ctx context.Context, audio []int16) (bool, error)
}
