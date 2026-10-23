package httpserver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/health"
)

func probe(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body)
}

func TestProbeRouter_Readiness(t *testing.T) {
	ok := health.CheckerFunc(func(context.Context) error { return nil })
	failing := health.CheckerFunc(func(context.Context) error { return errors.New("dial tcp 10.0.0.5:5432: secret detail") })
	hanging := health.CheckerFunc(func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })

	tests := []struct {
		name       string
		checks     map[string]health.Checker
		wantStatus int
		wantBody   string
	}{
		{"all dependencies up", map[string]health.Checker{"postgres": ok, "kafka": ok}, http.StatusOK, `"postgres":"ok"`},
		{"failing dependency", map[string]health.Checker{"postgres": failing, "kafka": ok}, http.StatusServiceUnavailable, `"postgres":"unavailable"`},
		{"dependency timeout", map[string]health.Checker{"postgres": ok, "kafka": hanging}, http.StatusServiceUnavailable, `"kafka":"unavailable"`},
		// The mock provider has no dependencies: serving means ready.
		{"no dependencies", nil, http.StatusOK, `"status":"ok"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ProbeRouter(discardLogger, health.NewReadiness(tt.checks, 20*time.Millisecond, discardLogger), nil)

			start := time.Now()
			status, body := probe(t, r, "/readyz")
			if status != tt.wantStatus || !strings.Contains(body, tt.wantBody) {
				t.Errorf("/readyz = %d %s, want %d containing %s", status, body, tt.wantStatus, tt.wantBody)
			}
			if strings.Contains(body, "secret") || strings.Contains(body, "10.0.0.5") {
				t.Errorf("/readyz leaks dependency error: %s", body)
			}
			if time.Since(start) > time.Second {
				t.Error("/readyz did not honor the readiness timeout")
			}
			if status, _ := probe(t, r, "/healthz"); status != http.StatusOK {
				t.Errorf("/healthz = %d, want 200 regardless of dependencies", status)
			}
		})
	}
}

func TestProbeRouter_Metrics(t *testing.T) {
	readiness := health.NewReadiness(nil, time.Second, discardLogger)

	withMetrics := ProbeRouter(discardLogger, readiness, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "metric_total 1\n")
	}))
	if status, body := probe(t, withMetrics, "/metrics"); status != http.StatusOK || body != "metric_total 1\n" {
		t.Errorf("/metrics = %d %q", status, body)
	}

	without := ProbeRouter(discardLogger, readiness, nil)
	if status, _ := probe(t, without, "/metrics"); status != http.StatusNotFound {
		t.Errorf("/metrics without a handler = %d, want 404", status)
	}
}
