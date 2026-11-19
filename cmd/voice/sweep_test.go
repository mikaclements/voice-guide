package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTheSweeperEndsIdleSessionsEveryIntervalUntilStopped(t *testing.T) {
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	swept := make(chan struct{}, 10)
	calls := 0
	endIdle := func(context.Context) (int, error) {
		calls++
		swept <- struct{}{}
		if calls == 1 {
			return 0, errors.New("connection refused")
		}
		return 2, nil
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		sweep(ctx, time.Millisecond, "idle sessions ended", endIdle, slog.New(slog.NewTextHandler(&logs, nil)))
	})
	for range 3 {
		select {
		case <-swept:
		case <-time.After(5 * time.Second):
			t.Fatal("waited 5s for a sweep")
		}
	}
	cancel()
	stopped := make(chan struct{})
	go func() { wg.Wait(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweeper didn't stop within 5s of being cancelled")
	}
	// A failed sweep is logged, and the next tries again.
	if got := logs.String(); !strings.Contains(got, "level=WARN") || !strings.Contains(got, "count=2") {
		t.Errorf("logs = %q, want the failure warned of and the sessions ended after it counted", got)
	}
}
