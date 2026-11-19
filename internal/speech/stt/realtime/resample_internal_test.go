package realtime

import (
	"math"
	"slices"
	"testing"
)

func sine(hz float64, rate, n int) []int16 {
	s := make([]int16, n)
	for i := range s {
		s[i] = int16(math.Round(10000 * math.Sin(2*math.Pi*hz*float64(i)/float64(rate))))
	}
	return s
}

func TestUpsampledSpeechIsTheSameSoundAt24kHz(t *testing.T) {
	var u upsampler
	got := u.up(sine(1000, 16000, 16000))
	want := sine(1000, 24000, len(got))
	for i := range got {
		// Interpolating between samples misses a 1 kHz sine by under 2% of its height.
		if d := math.Abs(float64(got[i]) - float64(want[i])); d > 200 {
			t.Fatalf("sample %d is %d, want %d within 200", i, got[i], want[i])
		}
	}
}

func TestEverySecondAt16kHzBecomesASecondAt24kHz(t *testing.T) {
	var u upsampler
	// The first second's last output waits for the sample after it.
	if a, b := len(u.up(make([]int16, 16000))), len(u.up(make([]int16, 16000))); a != 23999 || b != 24000 {
		t.Fatalf("two seconds gave %d then %d samples, want 23999 then 24000", a, b)
	}
}

func TestHowAudioIsCutIntoChunksChangesNothing(t *testing.T) {
	audio := sine(440, 16000, 4000)
	var whole upsampler
	want := whole.up(audio)
	var chunked upsampler
	var got []int16
	for _, n := range []int{1, 7, 512, 2, 1000, 2478} {
		got = append(got, chunked.up(audio[:n])...)
		audio = audio[n:]
	}
	if !slices.Equal(got, want) {
		t.Fatalf("chunked audio gave %d samples differing from the whole's %d", len(got), len(want))
	}
}
