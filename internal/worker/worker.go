// Package worker contains the background worker's lifecycle: wait for
// dependencies, run a setup step, then run the long-running tasks (outbox
// relay, queue consumer, maintenance) until shutdown.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"
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

// Task is a long-running part of the worker. Run blocks until ctx ends and
// then returns nil; returning earlier is a failure.
type Task struct {
	Name string
	Run  func(ctx context.Context) error
}

// Config lists what the worker runs once its dependencies are ready.
type Config struct {
	// Setup runs once before the tasks start, for example to create queue
	// topics. Its failure stops the worker.
	Setup func(ctx context.Context) error
	Tasks []Task
}

// Worker is the background processing loop.
type Worker struct {
	deps           Dependencies
	logger         *slog.Logger
	cfg            Config
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

// New returns a Worker with the default startup backoff.
func New(deps Dependencies, logger *slog.Logger, cfg Config) *Worker {
	return &Worker{
		deps:           deps,
		logger:         logger,
		cfg:            cfg,
		initialBackoff: DefaultInitialBackoff,
		maxBackoff:     DefaultMaxBackoff,
	}
}

// Run blocks until ctx is cancelled. It first waits for dependencies,
// retrying with capped exponential backoff, then runs Setup and every
// task. If a task fails or exits early, the others are cancelled and Run
// returns the error, so the process restarts instead of running half
// alive. It returns nil after a clean shutdown.
func (w *Worker) Run(ctx context.Context) error {
	if !w.waitForDependencies(ctx) {
		w.logger.Info("worker stopped before dependencies became ready")
		return nil
	}
	if w.cfg.Setup != nil {
		if err := w.cfg.Setup(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("worker setup: %w", err)
		}
	}
	w.logger.InfoContext(ctx, "worker ready", "tasks", len(w.cfg.Tasks))

	g, gctx := errgroup.WithContext(ctx)
	for _, t := range w.cfg.Tasks {
		g.Go(func() error {
			err := t.Run(gctx)
			switch {
			case err != nil:
				return fmt.Errorf("%s: %w", t.Name, err)
			case gctx.Err() == nil:
				return fmt.Errorf("%s: %w", t.Name, errExitedEarly)
			}
			return nil
		})
	}
	err := g.Wait()
	if err == nil {
		<-ctx.Done() // no tasks: idle until shutdown
	}
	w.logger.Info("worker stopped")
	return err
}

var errExitedEarly = errors.New("task exited before shutdown")

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
