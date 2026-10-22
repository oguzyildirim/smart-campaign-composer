// Package worker contains the background worker's lifecycle. In this
// milestone the worker only waits for its dependencies and idles until
// shutdown; queue consumption arrives with the delivery milestone.
package worker

import (
	"context"
	"log/slog"
	"time"
)

// Dependencies reports whether every dependency is reachable.
// *health.Readiness satisfies it.
type Dependencies interface {
	Check(ctx context.Context) (map[string]string, bool)
}

// Backoff bounds for dependency polling during startup.
const (
	DefaultInitialBackoff = 500 * time.Millisecond
	DefaultMaxBackoff     = 5 * time.Second
)

// Worker is the background processing loop.
type Worker struct {
	deps           Dependencies
	logger         *slog.Logger
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

// New returns a Worker with the default startup backoff.
func New(deps Dependencies, logger *slog.Logger) *Worker {
	return &Worker{
		deps:           deps,
		logger:         logger,
		initialBackoff: DefaultInitialBackoff,
		maxBackoff:     DefaultMaxBackoff,
	}
}

// Run blocks until ctx is cancelled. It first waits for dependencies,
// retrying with capped exponential backoff, and logs once they are ready.
// It returns nil on cancellation; there is no in-flight work to drain yet.
func (w *Worker) Run(ctx context.Context) error {
	if !w.waitForDependencies(ctx) {
		w.logger.Info("worker stopped before dependencies became ready")
		return nil
	}
	w.logger.InfoContext(ctx, "worker ready")

	<-ctx.Done()
	w.logger.Info("worker stopped")
	return nil
}

// waitForDependencies returns true once all checks pass, or false if ctx is
// cancelled first.
func (w *Worker) waitForDependencies(ctx context.Context) bool {
	backoff := w.initialBackoff
	for attempt := 1; ; attempt++ {
		statuses, ok := w.deps.Check(ctx)
		if ok {
			return true
		}
		w.logger.InfoContext(ctx, "waiting for dependencies",
			"attempt", attempt, "checks", statuses, "retry_in", backoff.String())

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
		backoff = min(backoff*2, w.maxBackoff)
	}
}
