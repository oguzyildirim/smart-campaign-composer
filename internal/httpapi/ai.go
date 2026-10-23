package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/httpserver"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
	"github.com/oguzyildirim/smart-campaign-composer/internal/strictjson"
)

// MaxBriefBodyBytes caps an AI request body. A maximal brief (1000
// characters, each escaped as \uXXXX in the worst case) fits.
const MaxBriefBodyBytes = 8 << 10

// StatusClientClosedRequest is recorded when the client disconnects before
// a response; nobody receives it, but access logs show what happened.
const StatusClientClosedRequest = 499

// codeInvalidRequest marks malformed requests (as opposed to invalid
// briefs).
const codeInvalidRequest = "invalid_request"

// AIGenerator turns briefs into validated proposals.
// *campaign.Generator implements it.
type AIGenerator interface {
	ProposeSegment(ctx context.Context, brief string) (campaign.SegmentProposal, error)
	GenerateDrafts(ctx context.Context, brief string, onDelta func(campaign.Delta)) (campaign.DraftsResult, error)
}

// BriefRequest is the body of both AI endpoints.
type BriefRequest struct {
	Brief string `json:"brief"`
}

// SegmentResponse is a validated segment proposal with its audience size.
type SegmentResponse struct {
	Rule                segment.Rule   `json:"rule"`
	Counts              segment.Counts `json:"counts"`
	UnsupportedCriteria []string       `json:"unsupported_criteria"`
	Explanation         string         `json:"explanation"`
}

// DraftsResponse is validated drafts. WorstCaseLengths counts each
// variable at its maximum length.
type DraftsResponse struct {
	Drafts           content.Drafts    `json:"drafts"`
	WorstCaseLengths content.WorstCase `json:"worst_case_lengths"`
}

// AIErrorResponse is the error body of the AI endpoints. Code is stable;
// Error is a human-readable, client-safe message.
type AIErrorResponse struct {
	Error               string   `json:"error"`
	Code                string   `json:"code"`
	UnsupportedCriteria []string `json:"unsupported_criteria,omitempty"`
}

var statusByCode = map[campaign.ErrorCode]int{
	campaign.CodeInvalidBrief:        http.StatusBadRequest,
	campaign.CodeInvalidOutput:       http.StatusBadGateway,
	campaign.CodeUnsupportedBrief:    http.StatusUnprocessableEntity,
	campaign.CodeRefused:             http.StatusUnprocessableEntity,
	campaign.CodeTimeout:             http.StatusGatewayTimeout,
	campaign.CodeUpstreamUnavailable: http.StatusServiceUnavailable,
}

// MountAI registers the AI generation routes on r. These are temporary
// endpoints; campaign endpoints replace them in the next milestone. The
// caller must give them a request timeout that covers every LLM attempt.
func MountAI(r chi.Router, gen AIGenerator, logger *slog.Logger) {
	h := aiHandler{gen: gen, logger: logger}
	r.Post("/ai/segment", h.segment)
	r.Post("/ai/drafts", h.drafts)
}

type aiHandler struct {
	gen    AIGenerator
	logger *slog.Logger
}

func (h aiHandler) segment(w http.ResponseWriter, r *http.Request) {
	brief, ok := h.readBrief(w, r)
	if !ok {
		return
	}
	p, err := h.gen.ProposeSegment(r.Context(), brief)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	unsupported := p.UnsupportedCriteria
	if unsupported == nil {
		unsupported = []string{}
	}
	httpserver.WriteJSON(w, http.StatusOK, SegmentResponse{
		Rule:                p.Rule,
		Counts:              p.Counts,
		UnsupportedCriteria: unsupported,
		Explanation:         p.Explanation,
	})
}

func (h aiHandler) drafts(w http.ResponseWriter, r *http.Request) {
	brief, ok := h.readBrief(w, r)
	if !ok {
		return
	}
	res, err := h.gen.GenerateDrafts(r.Context(), brief, nil)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, DraftsResponse{Drafts: res.Drafts, WorstCaseLengths: res.WorstCase})
}

// readBrief decodes the request strictly. The brief's content is validated
// by the domain, not here.
func (h aiHandler) readBrief(w http.ResponseWriter, r *http.Request) (string, bool) {
	body, status, msg := readJSONBody(w, r, MaxBriefBodyBytes)
	if status != 0 {
		writeAIError(w, status, codeInvalidRequest, msg, nil)
		return "", false
	}
	var req BriefRequest
	if err := strictjson.Decode(body, &req); err != nil {
		writeAIError(w, http.StatusBadRequest, codeInvalidRequest,
			`request body must be a JSON object with a single string field "brief"`, nil)
		return "", false
	}
	return req.Brief, true
}

func (h aiHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	ctx := r.Context()
	reqID := middleware.GetReqID(ctx)
	if gerr, ok := errors.AsType[*campaign.Error](err); ok {
		status, known := statusByCode[gerr.Code]
		if !known {
			status = http.StatusInternalServerError
		}
		level := slog.LevelInfo
		if status >= http.StatusInternalServerError {
			level = slog.LevelWarn
		}
		// gerr.Err holds adapter detail and validator messages; neither
		// contains the brief or generated text.
		h.logger.LogAttrs(ctx, level, "ai generation failed",
			slog.String("request_id", reqID), slog.String("code", string(gerr.Code)), slog.Any("error", gerr.Err))
		writeAIError(w, status, string(gerr.Code), gerr.Message, gerr.UnsupportedCriteria)
		return
	}
	if errors.Is(err, context.Canceled) {
		h.logger.InfoContext(ctx, "ai generation cancelled by client", "request_id", reqID)
		writeAIError(w, StatusClientClosedRequest, "cancelled", "request cancelled", nil)
		return
	}
	h.logger.ErrorContext(ctx, "ai generation failed", "request_id", reqID, "error", err)
	writeAIError(w, http.StatusInternalServerError, "internal", "internal server error", nil)
}

func writeAIError(w http.ResponseWriter, status int, code, msg string, unsupported []string) {
	httpserver.WriteJSON(w, status, AIErrorResponse{Error: msg, Code: code, UnsupportedCriteria: unsupported})
}
