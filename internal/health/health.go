// Package health implements liveness and readiness probes.
//
// Liveness reports only that the process is serving HTTP. Readiness runs
// every registered dependency check concurrently under a shared timeout and
// reports 503 when any of them fails. Check errors are logged server-side and
// never returned to clients.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Status values reported in probe responses.
const (
	StatusOK          = "ok"
	StatusUnavailable = "unavailable"
)

// Checker verifies that one dependency is reachable. Implementations must
// honor ctx cancellation.
type Checker interface {
	Check(ctx context.Context) error
}

// CheckerFunc adapts a function to the Checker interface.
type CheckerFunc func(ctx context.Context) error

// Check implements Checker.
func (f CheckerFunc) Check(ctx context.Context) error { return f(ctx) }

// Response is the JSON body of both probes.
type Response struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// Readiness evaluates a fixed set of named dependency checks.
type Readiness struct {
	checks  map[string]Checker
	timeout time.Duration
	logger  *slog.Logger
}

// NewReadiness returns a Readiness that gives all checks together at most
// timeout to complete. The checks map is copied.
func NewReadiness(checks map[string]Checker, timeout time.Duration, logger *slog.Logger) *Readiness {
	copied := make(map[string]Checker, len(checks))
	for name, c := range checks {
		copied[name] = c
	}
	return &Readiness{checks: copied, timeout: timeout, logger: logger}
}

// Check runs all checks and returns per-check statuses and whether every
// check passed.
func (r *Readiness) Check(ctx context.Context) (map[string]string, bool) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(r.checks))

	var wg sync.WaitGroup
	for name, c := range r.checks {
		wg.Go(func() {
			results <- result{name: name, err: runCheck(ctx, c)}
		})
	}
	wg.Wait()
	close(results)

	statuses := make(map[string]string, len(r.checks))
	healthy := true
	for res := range results {
		if res.err != nil {
			healthy = false
			statuses[res.name] = StatusUnavailable
			r.logger.WarnContext(ctx, "readiness check failed", "check", res.name, "error", res.err)
			continue
		}
		statuses[res.name] = StatusOK
	}
	return statuses, healthy
}

// runCheck returns as soon as either the check finishes or ctx expires, so
// a checker that ignores cancellation cannot stall the probe. The buffered
// channel lets such a goroutine exit once the checker eventually returns.
func runCheck(ctx context.Context, c Checker) error {
	done := make(chan error, 1)
	go func() { done <- c.Check(ctx) }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// LivenessHandler always reports 200 while the process can serve requests.
func LivenessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, Response{Status: StatusOK})
	}
}

// ReadinessHandler reports 200 when every check passes and 503 otherwise.
func (r *Readiness) ReadinessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		statuses, healthy := r.Check(req.Context())
		if !healthy {
			writeJSON(w, http.StatusServiceUnavailable, Response{Status: StatusUnavailable, Checks: statuses})
			return
		}
		writeJSON(w, http.StatusOK, Response{Status: StatusOK, Checks: statuses})
	}
}

func writeJSON(w http.ResponseWriter, status int, body Response) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body) // headers already sent; nothing useful to do on error
}
