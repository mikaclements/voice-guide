package silero_test

import (
	"encoding/binary"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/mikaclements/voice-guide/internal/speech"
	"github.com/mikaclements/voice-guide/internal/speech/detection/silero"
)

// recording returns Kokoro saying "I moved the team to weekly releases. It
// took about three months.", resampled to 16 kHz, with 0.5 s of silence
// before and 1 s after, in frames of 512 samples scaled to [-1, 1].
func recording(t *testing.T) [][]float32 {
	t.Helper()
	b, err := os.ReadFile("testdata/weekly-releases-16k.pcm")
	if err != nil {
		t.Fatalf("read the recording: %v", err)
	}
	var frames [][]float32
	for f := 0; f+2*speech.DetectionFrame <= len(b); f += 2 * speech.DetectionFrame {
		frame := make([]float32, speech.DetectionFrame)
		for i := range frame {
			frame[i] = float32(int16(binary.LittleEndian.Uint16(b[f+2*i:]))) / 32768
		}
		frames = append(frames, frame)
	}
	return frames
}

// original returns each frame's probability as Silero VAD v5's own ONNX
// model gives it: silero_vad.onnx from silero-vad 5.1.2, run once by
// onnxruntime in Python over the same frames, its state carried from frame to
// frame. That is the bare model on 512 samples, as govad takes them; Silero's
// own wrapper also puts the previous frame's last 64 samples before each
// frame, which govad can't take. Against the wrapper on this recording govad
// differs by at most 0.697 and 0.051 on average, and 5 of 166 frames fall on
// the other side of 0.5, all at the edges of speech. That is acceptable: an
// edge frame is 32 ms, against a 0.2 s pause, 0.3 s of audio kept from before
// speech starts, and the end-of-turn check deciding when a turn ends.
func original(t *testing.T) []float64 {
	t.Helper()
	b, err := os.ReadFile("testdata/weekly-releases-silero-v5.txt")
	if err != nil {
		t.Fatalf("read the original's probabilities: %v", err)
	}
	var probs []float64
	for line := range strings.Lines(string(b)) {
		p, err := strconv.ParseFloat(strings.TrimSpace(line), 64)
		if err != nil {
			t.Fatalf("read the original's probabilities: %v", err)
		}
		probs = append(probs, p)
	}
	return probs
}

// closeEnough is how far each frame's probability may be from the original's:
// a hundredth, far under the gap between speech and silence a threshold of
// 0.5 tells apart. On this recording the two differ by at most 0.000001, the
// precision the original's values are written to.
const closeEnough = 0.01

func TestSpeechIsJudgedAsSileroVADsOwnModelJudgesIt(t *testing.T) {
	d, err := silero.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	frames, want := recording(t), original(t)
	if len(frames) != len(want) {
		t.Fatalf("%d frames but %d probabilities", len(frames), len(want))
	}
	detector := d.Detector()
	largest, speech := 0.0, 0
	for i, frame := range frames {
		got := float64(detector.Speech(frame))
		largest = max(largest, math.Abs(got-want[i]))
		if math.Abs(got-want[i]) > closeEnough {
			t.Errorf("frame %d: %.6f, want %.6f, the original's", i, got, want[i])
		}
		if want[i] >= 0.5 {
			speech++
		}
	}
	t.Logf("largest difference from the original: %.6f over %d frames", largest, len(frames))
	// The recording is mostly speech, with silence at both ends.
	if speech < len(frames)/2 || speech == len(frames) {
		t.Errorf("the original judged %d of %d frames speech: not a recording of speech and silence", speech, len(frames))
	}
}

func TestEachConnectionsDetectorKeepsItsOwnState(t *testing.T) {
	d, err := silero.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	frames, want := recording(t), original(t)
	silence := make([]float32, speech.DetectionFrame)
	alone := d.Detector()
	var quiet []float32
	for range frames {
		quiet = append(quiet, alone.Speech(silence))
	}
	// Two connections' detectors at once, one hearing the speech and one silence: each
	// judges as though it were alone.
	talking, listening := d.Detector(), d.Detector()
	var wg sync.WaitGroup
	wg.Go(func() {
		for i, frame := range frames {
			if got := float64(talking.Speech(frame)); math.Abs(got-want[i]) > closeEnough {
				t.Errorf("talking frame %d: %.6f, want %.6f", i, got, want[i])
			}
		}
	})
	wg.Go(func() {
		for i := range frames {
			if got := listening.Speech(silence); got != quiet[i] {
				t.Errorf("silent frame %d: %.6f, want %.6f, as alone", i, got, quiet[i])
			}
		}
	})
	wg.Wait()
}
