package worker

import (
	"context"
	"errors"
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
	return newTestWorkerWith(deps, Config{})
}

func newTestWorkerWith(deps Dependencies, cfg Config) *Worker {
	w := New(deps, discardLogger, cfg)
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

func TestRun_SetupThenTasksUntilShutdown(t *testing.T) {
	var setupDone, started atomic.Int64
	block := func(ctx context.Context) error {
		if setupDone.Load() != 1 {
			return errors.New("task started before setup")
		}
		started.Add(1)
		<-ctx.Done()
		return nil
	}
	w := newTestWorkerWith(&fakeDeps{}, Config{
		Setup: func(context.Context) error { setupDone.Add(1); return nil },
		Tasks: []Task{{Name: "a", Run: block}, {Name: "b", Run: block}},
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := runAsync(ctx, w)
	deadline := time.Now().Add(2 * time.Second)
	for started.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if started.Load() != 2 {
		t.Fatalf("tasks started = %d, want 2", started.Load())
	}
	cancel()
	waitDone(t, done)
}

func TestRun_FailingTaskStopsTheWorker(t *testing.T) {
	stopped := make(chan struct{})
	w := newTestWorkerWith(&fakeDeps{}, Config{Tasks: []Task{
		{Name: "healthy", Run: func(ctx context.Context) error { <-ctx.Done(); close(stopped); return nil }},
		{Name: "broken", Run: func(context.Context) error { return errors.New("boom") }},
	}})
	err := w.Run(t.Context())
	if err == nil || err.Error() != "broken: boom" {
		t.Fatalf("Run() = %v, want the broken task's error", err)
	}
	<-stopped // the healthy task was cancelled
}

func TestRun_TaskExitingEarlyIsAnError(t *testing.T) {
	w := newTestWorkerWith(&fakeDeps{}, Config{Tasks: []Task{{Name: "quitter", Run: func(context.Context) error { return nil }}}})
	if err := w.Run(t.Context()); !errors.Is(err, errExitedEarly) {
		t.Fatalf("Run() = %v, want errExitedEarly", err)
	}
}

func TestRun_SetupFailureStopsTheWorker(t *testing.T) {
	ran := false
	w := newTestWorkerWith(&fakeDeps{}, Config{
		Setup: func(context.Context) error { return errors.New("no topics") },
		Tasks: []Task{{Name: "t", Run: func(context.Context) error { ran = true; return nil }}},
	})
	if err := w.Run(t.Context()); err == nil {
		t.Fatal("Run() = nil, want the setup error")
	}
	if ran {
		t.Error("tasks ran after a failed setup")
	}
}
