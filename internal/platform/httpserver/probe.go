package httpserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/oguzyildirim/smart-campaign-composer/internal/health"
)

// probeTimeout bounds a container healthcheck request.
const probeTimeout = 3 * time.Second

// Probe performs a GET against url and returns an error unless the response
// is 200. Binaries expose it as a "healthcheck" subcommand because the
// distroless runtime image ships no curl or wget.
func Probe(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build probe request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("probe %s: %w", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe %s: status %d", url, resp.StatusCode)
	}
	return nil
}

// ProbeRouter serves the operational endpoints of a binary without a
// public API (worker, mock provider): /healthz, /readyz, and /metrics
// when metrics is non-nil. Readiness bounds its own checks, so no request
// timeout is applied.
func ProbeRouter(logger *slog.Logger, readiness *health.Readiness, metrics http.Handler) chi.Router {
	r := NewRouter(logger, 0)
	r.Get("/healthz", health.LivenessHandler())
	r.Get("/readyz", readiness.ReadinessHandler())
	if metrics != nil {
		r.Method(http.MethodGet, "/metrics", metrics)
	}
	return r
}
