package speech

import (
	"testing"
	"time"
)

func TestTheSessionIsMarkedActiveATenthOfTheIdleTimeApartAtMost30Seconds(t *testing.T) {
	for idle, want := range map[time.Duration]time.Duration{
		5 * time.Minute:  30 * time.Second,
		time.Hour:        30 * time.Second,
		25 * time.Second: 2500 * time.Millisecond,
	} {
		if got := markEvery(idle); got != want {
			t.Errorf("markEvery(%s) = %s, want %s", idle, got, want)
		}
	}
}

func TestSpeechMarksTheSessionAtFirstThenOnlyOnceAnIntervalHasPassed(t *testing.T) {
	start := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	m := marker{every: 30 * time.Second}
	for _, step := range []struct {
		at  time.Duration
		due bool
	}{
		{0, true},
		{10 * time.Second, false},
		{29 * time.Second, false},
		{30 * time.Second, true},
		{59 * time.Second, false},
		{2 * time.Minute, true},
	} {
		if got := m.due(start.Add(step.at)); got != step.due {
			t.Errorf("speech at %s: due %v, want %v", step.at, got, step.due)
		}
	}
}
