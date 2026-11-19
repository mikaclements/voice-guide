// Package silero notices speech with Silero VAD's model, version 5, run in
// pure Go by govad: speech's speech detection.
package silero

import (
	"fmt"

	"github.com/zserge/govad"

	"github.com/mikaclements/voice-guide/internal/speech"
)

// Detection judges speech with the model's weights, loaded once and shared by
// every audio connection's detector: 1.2 MB, which a connection each would
// multiply.
type Detection struct {
	model govad.VAD
}

// New loads the model's weights, which govad carries in the binary.
func New() (*Detection, error) {
	v, err := govad.New()
	if err != nil {
		return nil, fmt.Errorf("load Silero's weights: %w", err)
	}
	return &Detection{model: *v}, nil
}

// Detector returns a detector for one audio connection. govad keeps its weights in
// slices and its state in arrays, so a copy of the loaded model shares the
// weights, which judging only reads, and has a state of its own.
func (d *Detection) Detector() speech.Detector {
	s := &detector{model: d.model}
	s.model.Reset()
	return s
}

type detector struct {
	model govad.VAD
}

func (s *detector) Speech(frame []float32) float32 { return s.model.Process(frame) }
