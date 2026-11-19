package speech

// DetectionFrame is how many samples speech detection judges at once: 512 at
// ListenRate, 32 ms, the frame Silero's model takes at 16 kHz.
const DetectionFrame = 512

// SpeechDetection judges how likely the user's audio holds speech: the swap
// point for noticing speech.
type SpeechDetection interface {
	// Detector starts judging one audio connection's audio. A detector
	// judges each frame in the light of the frames before, so each
	// connection has its own.
	Detector() Detector
}

// Detector judges one connection's audio, a frame at a time, in order. It is
// not safe for concurrent use.
type Detector interface {
	// Speech returns the probability, from 0 to 1, that frame holds speech:
	// DetectionFrame samples at ListenRate, mono, from -1 to 1.
	Speech(frame []float32) float32
}
