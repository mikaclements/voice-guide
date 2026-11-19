// Package fake judges every turn as it is told to: speech's fake end-of-turn check, for the
// core's tests and for load tests.
package fake

import (
	"context"

	"github.com/mikaclements/voice-guide/internal/speech"
)

// EndOfTurn answers Answer to every check, or fails with Fail when it is set.
type EndOfTurn struct {
	Answer bool
	Fail   error
}

var _ speech.EndOfTurn = EndOfTurn{}

func (f EndOfTurn) Finished(context.Context, []int16) (bool, error) {
	return f.Answer && f.Fail == nil, f.Fail
}
