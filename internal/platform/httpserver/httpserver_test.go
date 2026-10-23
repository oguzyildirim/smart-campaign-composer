package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var discardLogger = slog.New(slog.DiscardHandler)

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

func TestServe_GracefulShutdownWithinTimeout(t *testing.T) {
	ln := listen(t)
	srv := New(ln.Addr().String(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), discardLogger)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv, ln, time.Second, discardLogger) }()

	if err := Probe(t.Context(), "http://"+ln.Addr().String()+"/"); err == nil {
		t.Fatal("Probe() succeeded on 204, want error for non-200")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() returned %v after cancellation, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve() did not return within the shutdown bound")
	}
}

func TestServe_ShutdownTimeoutBoundsSlowRequests(t *testing.T) {
	ln := listen(t)
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	srv := New(ln.Addr().String(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release // a request that never finishes on its own
	}), discardLogger)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv, ln, 100*time.Millisecond, discardLogger) }()

	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Serve() = nil, want shutdown deadline error")
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("shutdown took %v, want about 100ms", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve() hung past the shutdown timeout")
	}
}

func TestRun_ListenError(t *testing.T) {
	ln := listen(t)
	t.Cleanup(func() { _ = ln.Close() })

	srv := New(ln.Addr().String(), http.NotFoundHandler(), discardLogger)
	if err := Run(t.Context(), srv, time.Second, discardLogger); err == nil {
		t.Fatal("Run() on an address in use returned nil")
	}
}

func TestRecoverer_ReturnsGeneric500(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	r := NewRouter(logger, time.Second)
	r.Get("/boom", func(http.ResponseWriter, *http.Request) {
		panic("secret internal detail")
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret internal detail") {
		t.Errorf("response leaks panic value: %s", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "panic recovered") {
		t.Errorf("panic was not logged: %s", logs.String())
	}
}

func TestRouter_RequestIDAndAccessLog(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	r := NewRouter(logger, time.Second)
	r.Get("/things", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/things?email=someone@example.com", nil))

	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("access log is not a single JSON line: %v (%q)", err, logs.String())
	}
	if entry["status"] != float64(http.StatusTeapot) || entry["path"] != "/things" {
		t.Errorf("access log = %v", entry)
	}
	if id, _ := entry["request_id"].(string); id == "" {
		t.Errorf("access log has no request_id: %v", entry)
	}
	if strings.Contains(logs.String(), "someone@example.com") {
		t.Errorf("access log contains query string data: %s", logs.String())
	}
}

func TestRequestLogger_ProbePathsLogAtDebug(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	r := NewRouter(logger, time.Second)
	r.Get("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if logs.Len() != 0 {
		t.Errorf("probe request logged at info level: %s", logs.String())
	}
}

func TestRouter_NotFoundIsJSON(t *testing.T) {
	r := NewRouter(discardLogger, time.Second)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missing", nil))

	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("got %d %q, want 404 application/json", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestRouter_RequestTimeout(t *testing.T) {
	r := NewRouter(discardLogger, 20*time.Millisecond)
	r.Get("/slow", func(_ http.ResponseWriter, req *http.Request) {
		<-req.Context().Done()
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slow", nil))

	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", rec.Code)
	}
}

func TestRouter_ZeroTimeoutLeavesDeadlinesToRoutes(t *testing.T) {
	r := NewRouter(discardLogger, 0)
	var hasDeadline bool
	r.Get("/x", func(_ http.ResponseWriter, req *http.Request) {
		_, hasDeadline = req.Context().Deadline()
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if hasDeadline {
		t.Error("NewRouter(_, 0) set a request deadline")
	}
}

func TestProbe(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(okSrv.Close)
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(failSrv.Close)

	if err := Probe(t.Context(), okSrv.URL); err != nil {
		t.Errorf("Probe(200) = %v, want nil", err)
	}
	if err := Probe(t.Context(), failSrv.URL); err == nil {
		t.Error("Probe(503) = nil, want error")
	}
	if err := Probe(t.Context(), "http://127.0.0.1:1/"); err == nil {
		t.Error("Probe(unreachable) = nil, want error")
	}
}
