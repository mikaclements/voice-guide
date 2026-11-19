// Package fake judges speech by a script: speech's fake speech detection,
// for the core's tests and for load tests.
package fake

import "github.com/mikaclements/voice-guide/internal/speech"

// Detection gives every audio connection's detector the same script: the probability of each
// frame in turn, and 0, silence, for every frame after it. With no script,
// every frame is silence.
type Detection struct {
	Script []float32
}

func (d Detection) Detector() speech.Detector { return &detector{script: d.Script} }

type detector struct {
	script []float32
	next   int
}

func (s *detector) Speech([]float32) float32 {
	if s.next == len(s.script) {
		return 0
	}
	s.next++
	return s.script[s.next-1]
}
