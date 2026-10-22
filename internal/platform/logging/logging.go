// Package logging builds the structured JSON logger used by every binary.
package logging

import (
	"io"
	"log/slog"
)

// New returns a JSON logger writing to w at level, tagged with service.
func New(w io.Writer, service string, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})).
		With("service", service)
}
