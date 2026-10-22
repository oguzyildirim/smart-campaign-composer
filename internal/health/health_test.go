package health

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var discardLogger = slog.New(slog.DiscardHandler)

func ok() Checker { return CheckerFunc(func(context.Context) error { return nil }) }

func failing(msg string) Checker {
	return CheckerFunc(func(context.Context) error { return errors.New(msg) })
}

// blocking honors cancellation but never succeeds on its own.
func blocking() Checker {
	return CheckerFunc(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
}

func serve(t *testing.T, h http.Handler) (*httptest.ResponseRecorder, Response) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	var body Response
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%q)", err, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	return rec, body
}

func TestLivenessHandler(t *testing.T) {
	rec, body := serve(t, LivenessHandler())
	if rec.Code != http.StatusOK || body.Status != StatusOK {
		t.Errorf("liveness = %d %+v, want 200 ok", rec.Code, body)
	}
}

func TestReadinessHandler(t *testing.T) {
	tests := []struct {
		name       string
		checks     map[string]Checker
		wantCode   int
		wantStatus string
		wantChecks map[string]string
	}{
		{
			name:       "all checks pass",
			checks:     map[string]Checker{"postgres": ok(), "kafka": ok()},
			wantCode:   http.StatusOK,
			wantStatus: StatusOK,
			wantChecks: map[string]string{"postgres": StatusOK, "kafka": StatusOK},
		},
		{
			name:       "one check fails",
			checks:     map[string]Checker{"postgres": failing("dial tcp 10.0.0.5:5432: connection refused"), "kafka": ok()},
			wantCode:   http.StatusServiceUnavailable,
			wantStatus: StatusUnavailable,
			wantChecks: map[string]string{"postgres": StatusUnavailable, "kafka": StatusOK},
		},
		{
			name:       "check times out",
			checks:     map[string]Checker{"postgres": ok(), "kafka": blocking()},
			wantCode:   http.StatusServiceUnavailable,
			wantStatus: StatusUnavailable,
			wantChecks: map[string]string{"postgres": StatusOK, "kafka": StatusUnavailable},
		},
		{
			name:       "no checks registered",
			checks:     nil,
			wantCode:   http.StatusOK,
			wantStatus: StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewReadiness(tt.checks, 50*time.Millisecond, discardLogger)
			rec, body := serve(t, r.ReadinessHandler())

			if rec.Code != tt.wantCode {
				t.Errorf("status code = %d, want %d", rec.Code, tt.wantCode)
			}
			if body.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", body.Status, tt.wantStatus)
			}
			if len(body.Checks) != len(tt.wantChecks) {
				t.Errorf("checks = %v, want %v", body.Checks, tt.wantChecks)
			}
			for name, want := range tt.wantChecks {
				if body.Checks[name] != want {
					t.Errorf("checks[%s] = %q, want %q", name, body.Checks[name], want)
				}
			}
		})
	}
}

func TestReadinessHandler_DoesNotLeakErrorDetails(t *testing.T) {
	r := NewReadiness(map[string]Checker{
		"postgres": failing("password authentication failed for user app at 10.0.0.5:5432"),
	}, time.Second, discardLogger)

	rec := httptest.NewRecorder()
	r.ReadinessHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	for _, leak := range []string{"password", "10.0.0.5", "5432", "authentication"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("response body leaks %q: %s", leak, rec.Body.String())
		}
	}
}

func TestReadiness_TimeoutBoundsCheckerThatIgnoresContext(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stubborn := CheckerFunc(func(context.Context) error {
		<-release // ignores ctx entirely
		return nil
	})

	r := NewReadiness(map[string]Checker{"stubborn": stubborn}, 50*time.Millisecond, discardLogger)

	start := time.Now()
	statuses, healthy := r.Check(t.Context())
	elapsed := time.Since(start)

	if healthy || statuses["stubborn"] != StatusUnavailable {
		t.Errorf("Check() = %v, %v; want stubborn unavailable", statuses, healthy)
	}
	if elapsed > time.Second {
		t.Errorf("Check() took %v, want it bounded by the 50ms timeout", elapsed)
	}
}

func TestReadiness_RespectsCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	r := NewReadiness(map[string]Checker{"slow": blocking()}, time.Minute, discardLogger)
	if _, healthy := r.Check(ctx); healthy {
		t.Error("Check() with cancelled context reported healthy")
	}
}
