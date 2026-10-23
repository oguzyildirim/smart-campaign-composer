// Package httpapi implements the versioned JSON API (/api/v1). Handlers
// translate HTTP to calls on domain services and map domain errors to
// status codes; they hold no business rules of their own.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/httpserver"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

// MaxSegmentBodyBytes caps a segment rule request body. A maximal valid
// rule (10 conditions, 20 cities of 64 characters) is well under this.
const MaxSegmentBodyBytes = 16 << 10

// SegmentPreviewer validates a rule and sizes its audience.
// *segment.Service implements it.
type SegmentPreviewer interface {
	Preview(ctx context.Context, data []byte) (segment.Rule, segment.Counts, error)
}

// PreviewResponse is the body of a successful segment preview.
type PreviewResponse struct {
	Rule   segment.Rule   `json:"rule"`
	Counts segment.Counts `json:"counts"`
}

// ValidationErrorResponse is the body of a 400 caused by an invalid rule.
type ValidationErrorResponse struct {
	Error   string               `json:"error"`
	Details []segment.FieldError `json:"details"`
}

// MountSegments registers the segment routes on r.
func MountSegments(r chi.Router, previewer SegmentPreviewer, logger *slog.Logger) {
	h := segmentHandler{previewer: previewer, logger: logger}
	r.Post("/segments/preview", h.preview)
}

type segmentHandler struct {
	previewer SegmentPreviewer
	logger    *slog.Logger
}

func (h segmentHandler) preview(w http.ResponseWriter, r *http.Request) {
	body, status, msg := readJSONBody(w, r, MaxSegmentBodyBytes)
	if status != 0 {
		httpserver.WriteError(w, status, msg)
		return
	}

	rule, counts, err := h.previewer.Preview(r.Context(), body)
	if err != nil {
		if verr, ok := errors.AsType[*segment.ValidationError](err); ok {
			httpserver.WriteJSON(w, http.StatusBadRequest, ValidationErrorResponse{
				Error:   "invalid segment rule",
				Details: verr.Errors,
			})
			return
		}
		h.logger.ErrorContext(r.Context(), "segment preview failed",
			"request_id", middleware.GetReqID(r.Context()),
			"error", err,
		)
		httpserver.WriteError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, PreviewResponse{Rule: rule, Counts: counts})
}
