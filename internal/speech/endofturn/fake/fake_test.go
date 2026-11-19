package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mikaclements/voice-guide/internal/speech/endofturn/fake"
)

func TestTheFakeAnswersAsToldOrFails(t *testing.T) {
	broken := errors.New("broken")
	for _, c := range []struct {
		check fake.EndOfTurn
		want  bool
		err   error
	}{
		{fake.EndOfTurn{Answer: true}, true, nil},
		{fake.EndOfTurn{}, false, nil},
		{fake.EndOfTurn{Answer: true, Fail: broken}, false, broken},
	} {
		if got, err := c.check.Finished(context.Background(), nil); got != c.want || !errors.Is(err, c.err) {
			t.Errorf("%+v answered %v (%v), want %v (%v)", c.check, got, err, c.want, c.err)
		}
	}
}
