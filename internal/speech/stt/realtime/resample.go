package realtime

import (
	"math"
	"slices"
)

// upsampler turns 16 kHz samples into 24 kHz ones across the chunks of a stream. Each output
// sample lies two thirds of an input sample after the one before and is interpolated between
// its two neighbours. That leaves faint images of the speech between 8 and 12 kHz, which do no
// harm here: recognisers read 16 kHz audio, so the service filters them out going back down. It
// keeps the last sample of the chunk before, and where the next output lies, in thirds of a
// sample from the first sample held.
type upsampler struct {
	last []int16
	next int
}

func (u *upsampler) up(chunk []int16) []int16 {
	samples := slices.Concat(u.last, chunk)
	out := make([]int16, 0, len(chunk)*3/2+1)
	// An output is made once the sample after it has arrived, so each lags by one input sample.
	for ; u.next/3+1 < len(samples); u.next += 2 {
		i, third := u.next/3, float64(u.next%3)
		a, b := float64(samples[i]), float64(samples[i+1])
		out = append(out, int16(math.Round(a+(b-a)*third/3)))
	}
	if len(samples) > 0 {
		u.next -= 3 * (len(samples) - 1)
		u.last = append(u.last[:0], samples[len(samples)-1])
	}
	return out
}
