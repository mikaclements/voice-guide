package jobs

import (
	"context"
	"time"
)

// RunOnce claims a job that is due and runs it, and says whether there was
// one, so a test steps the runner one job at a time.
func RunOnce(r *Runner, ctx context.Context) bool {
	ran, _ := r.once(ctx)
	return ran
}

// Wait is the wait after the given attempt of a job of r's fails.
func Wait(r *Runner, attempt int) time.Duration { return r.wait(attempt) }

// LongestWait bounds the wait after one failed run.
const LongestWait = longestWait
