package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// quietPaths are probe endpoints polled every few seconds by Docker; their
// access logs always go to debug level to keep the info stream useful.
// Failing dependency checks are logged separately by the health package.
var quietPaths = map[string]bool{"/healthz": true, "/readyz": true}

// NewRouter returns a chi router with the base middleware stack: request ID,
// panic recovery, access logging, and a per-request timeout.
func NewRouter(logger *slog.Logger, requestTimeout time.Duration) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(RequestLogger(logger))
	r.Use(Recoverer(logger))
	r.Use(middleware.Timeout(requestTimeout))
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusNotFound, "not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	})
	return r
}

// RequestLogger logs one structured line per request. It records the path
// without the query string, which may later carry user data.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			level := slog.LevelInfo
			switch {
			case quietPaths[r.URL.Path]:
				level = slog.LevelDebug
			case status >= http.StatusInternalServerError:
				level = slog.LevelError
			}
			logger.LogAttrs(r.Context(), level, "http request",
				slog.String("request_id", middleware.GetReqID(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", status),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
			)
		})
	}
}

// Recoverer converts a handler panic into a generic 500 response and logs
// the panic with its stack trace. Clients never see panic details.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec) // net/http uses this sentinel to abort silently; keep its semantics
				}
				logger.ErrorContext(ctx, "panic recovered",
					"request_id", middleware.GetReqID(ctx),
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				WriteError(w, http.StatusInternalServerError, "internal server error")
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// errorBody is the uniform JSON error envelope.
type errorBody struct {
	Error string `json:"error"`
}

// WriteError writes a JSON error response. message must be safe to show to
// clients; never pass internal error text.
func WriteError(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, errorBody{Error: message})
}

// WriteJSON writes body as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body) // headers already sent; nothing useful to do on error
}
