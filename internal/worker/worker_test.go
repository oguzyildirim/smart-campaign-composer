package worker

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

var discardLogger = slog.New(slog.DiscardHandler)

// fakeDeps becomes healthy after failuresBefore failed checks; -1 never does.
type fakeDeps struct {
	failuresBefore int64
	calls          atomic.Int64
}

func (f *fakeDeps) Check(context.Context) (map[string]string, bool) {
	n := f.calls.Add(1)
	if f.failuresBefore < 0 || n <= f.failuresBefore {
		return map[string]string{"postgres": "unavailable"}, false
	}
	return map[string]string{"postgres": "ok"}, true
}

func newTestWorker(deps Dependencies) *Worker {
	w := New(deps, discardLogger)
	w.initialBackoff = time.Millisecond
	w.maxBackoff = 4 * time.Millisecond
	return w
}

func runAsync(ctx context.Context, w *Worker) <-chan error {
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	return done
}

func waitDone(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after cancellation")
	}
}

func TestRun_RetriesUntilDependenciesReady(t *testing.T) {
	deps := &fakeDeps{failuresBefore: 3}
	ctx, cancel := context.WithCancel(t.Context())
	done := runAsync(ctx, newTestWorker(deps))

	deadline := time.Now().Add(2 * time.Second)
	for deps.calls.Load() < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("dependencies checked %d times, want 4", deps.calls.Load())
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	waitDone(t, done)

	// Once ready, the worker stops polling.
	if got := deps.calls.Load(); got != 4 {
		t.Errorf("dependency checks = %d, want exactly 4", got)
	}
}

func TestRun_StopsWhileWaitingForDependencies(t *testing.T) {
	deps := &fakeDeps{failuresBefore: -1}
	ctx, cancel := context.WithCancel(t.Context())
	done := runAsync(ctx, newTestWorker(deps))

	time.Sleep(20 * time.Millisecond)
	cancel()
	waitDone(t, done)

	if deps.calls.Load() == 0 {
		t.Error("worker never checked its dependencies")
	}
}

func TestRun_BackoffIsCapped(t *testing.T) {
	deps := &fakeDeps{failuresBefore: -1}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	if err := newTestWorker(deps).Run(ctx); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	// With a 4ms cap, 100ms allows many attempts; an uncapped doubling
	// from 1ms would manage only about seven.
	if got := deps.calls.Load(); got < 10 {
		t.Errorf("dependency checks = %d in 100ms, want >= 10 with capped backoff", got)
	}
}
