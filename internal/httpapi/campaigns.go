package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/httpserver"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
	"github.com/oguzyildirim/smart-campaign-composer/internal/strictjson"
)

// Request body limits. A maximal brief (1000 characters, each escaped as
// \uXXXX in the worst case) fits MaxBriefBodyBytes; a maximal edit
// (content at the service's hard caps, fully escaped) fits
// MaxEditBodyBytes.
const (
	MaxBriefBodyBytes   = 8 << 10
	MaxEditBodyBytes    = 32 << 10
	MaxApproveBodyBytes = 4 << 10
)

// StatusClientClosedRequest is recorded when the client disconnects before
// a response; nobody receives it, but access logs show what happened.
const StatusClientClosedRequest = 499

// CampaignService runs the campaign lifecycle. *campaign.Service
// implements it.
type CampaignService interface {
	Create(ctx context.Context, brief string) (campaign.Campaign, error)
	Get(ctx context.Context, id campaign.ID) (campaign.Campaign, error)
	List(ctx context.Context) ([]campaign.Campaign, error)
	Edit(ctx context.Context, id campaign.ID, e campaign.Edit) (campaign.Campaign, error)
	Preview(ctx context.Context, id campaign.ID) (campaign.Preview, error)
	Approve(ctx context.Context, id campaign.ID, a campaign.Approval) (campaign.Campaign, error)
	BeginGeneration(ctx context.Context, id campaign.ID) (*campaign.Generation, error)
}

// CampaignRoutes configures the campaign endpoints.
type CampaignRoutes struct {
	Service CampaignService
	Logger  *slog.Logger
	// RequestTimeout bounds reads, edits, previews, and approval.
	RequestTimeout time.Duration
	// CreateTimeout bounds creation, which calls the LLM.
	CreateTimeout time.Duration
	// Heartbeat is the interval of SSE keep-alive comments during draft
	// generation. The stream has no request timeout; the service bounds
	// the generation itself.
	Heartbeat time.Duration
	// Now reports the current time for derived fields; nil means time.Now.
	Now func() time.Time
}

// MountCampaigns registers the campaign routes on r, each with its own
// timeout.
func MountCampaigns(r chi.Router, cfg CampaignRoutes) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	h := campaignHandler{svc: cfg.Service, logger: cfg.Logger, heartbeat: cfg.Heartbeat, now: cfg.Now}
	timeout := middleware.Timeout(cfg.RequestTimeout)
	r.With(middleware.Timeout(cfg.CreateTimeout)).Post("/campaigns", h.create)
	r.With(timeout).Get("/campaigns", h.list)
	r.With(timeout).Get("/campaigns/{id}", h.get)
	r.With(timeout).Patch("/campaigns/{id}", h.edit)
	r.With(timeout).Get("/campaigns/{id}/preview", h.preview)
	r.With(timeout).Post("/campaigns/{id}/approve", h.approve)
	r.Post("/campaigns/{id}/drafts", h.generate)
}

type campaignHandler struct {
	svc       CampaignService
	logger    *slog.Logger
	heartbeat time.Duration
	now       func() time.Time
}

// CreateRequest is the body of POST /campaigns.
type CreateRequest struct {
	Brief string `json:"brief"`
}

// EditRequest is the body of PATCH /campaigns/{id}. Every field is
// required: an edit replaces the content and channel selection.
type EditRequest struct {
	Version      *int      `json:"version"`
	Channels     *[]string `json:"channels"`
	SMSBody      *string   `json:"sms_body"`
	EmailSubject *string   `json:"email_subject"`
	EmailBody    *string   `json:"email_body"`
}

// ApproveRequest is the body of POST /campaigns/{id}/approve: the version
// and content hash the reviewer saw, and who approves.
type ApproveRequest struct {
	Version     *int    `json:"version"`
	ContentHash *string `json:"content_hash"`
	ApprovedBy  *string `json:"approved_by"`
}

// CampaignResponse is a campaign as the API returns it.
type CampaignResponse struct {
	ID          string              `json:"id"`
	Brief       string              `json:"brief"`
	Status      campaign.Status     `json:"status"`
	Audience    AudienceResponse    `json:"audience"`
	Channels    []content.Channel   `json:"channels"`
	Drafts      *DraftsBody         `json:"drafts"`
	Validation  *ValidationResponse `json:"validation"`
	Version     int                 `json:"version"`
	ContentHash string              `json:"content_hash"`
	Generating  bool                `json:"generating"`
	ApprovedBy  *string             `json:"approved_by"`
	ApprovedAt  *time.Time          `json:"approved_at"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

// AudienceResponse is the audience proposal fixed at creation. Summary is
// the rule in plain language, one line per condition.
type AudienceResponse struct {
	Rule                segment.Rule   `json:"rule"`
	Summary             []string       `json:"summary"`
	Explanation         string         `json:"explanation"`
	UnsupportedCriteria []string       `json:"unsupported_criteria"`
	Counts              segment.Counts `json:"counts"`
}

// DraftsBody is message content, named like the edit request fields.
type DraftsBody struct {
	SMSBody      string `json:"sms_body"`
	EmailSubject string `json:"email_subject"`
	EmailBody    string `json:"email_body"`
}

// ValidationResponse reports the content checks for the selected channels.
// Limits are the channel limits in characters, so clients display them
// without keeping their own copy of the rules.
type ValidationResponse struct {
	Valid            bool                 `json:"valid"`
	Errors           []content.FieldError `json:"errors"`
	WorstCaseLengths content.WorstCase    `json:"worst_case_lengths"`
	Limits           content.WorstCase    `json:"limits"`
}

var fieldLimits = content.WorstCase{
	SMSBody:      content.MaxSMSRunes,
	EmailSubject: content.MaxEmailSubjectRunes,
	EmailBody:    content.MaxEmailBodyRunes,
}

// CampaignSummary is a list entry.
type CampaignSummary struct {
	ID         string            `json:"id"`
	Brief      string            `json:"brief"`
	Status     campaign.Status   `json:"status"`
	Counts     segment.Counts    `json:"counts"`
	Channels   []content.Channel `json:"channels"`
	ApprovedAt *time.Time        `json:"approved_at"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// ListResponse is the body of GET /campaigns.
type ListResponse struct {
	Campaigns []CampaignSummary `json:"campaigns"`
}

// ContentPreviewResponse is content rendered for sample recipients. Version and
// ContentHash identify the content that was rendered.
type ContentPreviewResponse struct {
	Version     int                `json:"version"`
	ContentHash string             `json:"content_hash"`
	Validation  ValidationResponse `json:"validation"`
	Channels    []ChannelPreview   `json:"channels"`
}

// ChannelPreview is one selected channel's audience and rendered samples.
// LongestValues holds the longest first_name and city, in characters,
// among the channel's consented recipients.
type ChannelPreview struct {
	Channel       content.Channel          `json:"channel"`
	Recipients    int64                    `json:"recipients"`
	LongestValues map[content.Variable]int `json:"longest_values"`
	Samples       []SamplePreview          `json:"samples"`
}

// SamplePreview is content rendered for one fake recipient. Only the
// personalization values are exposed, never contact details.
type SamplePreview struct {
	UserID    int64           `json:"user_id"`
	FirstName string          `json:"first_name"`
	City      string          `json:"city"`
	Fields    []RenderedField `json:"fields"`
}

// RenderedField is a field after personalization with its final length.
type RenderedField struct {
	Field     string `json:"field"`
	Text      string `json:"text"`
	Length    int    `json:"length"`
	Limit     int    `json:"limit"`
	OverLimit bool   `json:"over_limit"`
}

// ErrorResponse is the error body of the campaign endpoints. Code is
// stable; Error is a human-readable, client-safe message.
type ErrorResponse struct {
	Error               string               `json:"error"`
	Code                string               `json:"code"`
	Details             []content.FieldError `json:"details,omitempty"`
	UnsupportedCriteria []string             `json:"unsupported_criteria,omitempty"`
}

// codeInvalidRequest marks malformed requests (as opposed to invalid
// briefs).
const codeInvalidRequest = string(campaign.CodeInvalidRequest)

var statusByCode = map[campaign.ErrorCode]int{
	campaign.CodeInvalidBrief:         http.StatusBadRequest,
	campaign.CodeInvalidRequest:       http.StatusBadRequest,
	campaign.CodeNotFound:             http.StatusNotFound,
	campaign.CodeInvalidState:         http.StatusConflict,
	campaign.CodeVersionConflict:      http.StatusConflict,
	campaign.CodeContentChanged:       http.StatusConflict,
	campaign.CodeGenerationInProgress: http.StatusConflict,
	campaign.CodeInvalidContent:       http.StatusUnprocessableEntity,
	campaign.CodeNoRecipients:         http.StatusUnprocessableEntity,
	campaign.CodeUnsupportedBrief:     http.StatusUnprocessableEntity,
	campaign.CodeRefused:              http.StatusUnprocessableEntity,
	campaign.CodeInvalidOutput:        http.StatusBadGateway,
	campaign.CodeUpstreamUnavailable:  http.StatusServiceUnavailable,
	campaign.CodeTimeout:              http.StatusGatewayTimeout,
}

func (h campaignHandler) create(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if !h.decode(w, r, MaxBriefBodyBytes, &req, `request body must be a JSON object with a single string field "brief"`) {
		return
	}
	c, err := h.svc.Create(r.Context(), req.Brief)
	if err != nil {
		h.writeError(w, r, "create campaign", err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, h.campaignResponse(c))
}

func (h campaignHandler) list(w http.ResponseWriter, r *http.Request) {
	cs, err := h.svc.List(r.Context())
	if err != nil {
		h.writeError(w, r, "list campaigns", err)
		return
	}
	out := ListResponse{Campaigns: make([]CampaignSummary, len(cs))}
	for i, c := range cs {
		out.Campaigns[i] = CampaignSummary{
			ID: string(c.ID), Brief: c.Brief, Status: c.Status, Counts: c.Counts,
			Channels: nonNilChannels(c.Channels), ApprovedAt: optionalTime(c.ApprovedAt),
			CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		}
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

func (h campaignHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := h.campaignID(w, r)
	if !ok {
		return
	}
	c, err := h.svc.Get(r.Context(), id)
	if err != nil {
		h.writeError(w, r, "get campaign", err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, h.campaignResponse(c))
}

func (h campaignHandler) edit(w http.ResponseWriter, r *http.Request) {
	id, ok := h.campaignID(w, r)
	if !ok {
		return
	}
	var req EditRequest
	if !h.decode(w, r, MaxEditBodyBytes, &req,
		"request body must be a JSON object with version, channels, sms_body, email_subject, and email_body") {
		return
	}
	var missing []content.FieldError
	require := func(name string, present bool) {
		if !present {
			missing = append(missing, content.FieldError{Path: name, Message: "is required"})
		}
	}
	require("version", req.Version != nil)
	require("channels", req.Channels != nil)
	require("sms_body", req.SMSBody != nil)
	require("email_subject", req.EmailSubject != nil)
	require("email_body", req.EmailBody != nil)
	if len(missing) > 0 {
		writeCampaignError(w, http.StatusBadRequest, codeInvalidRequest, "request is missing required fields", missing, nil)
		return
	}
	c, err := h.svc.Edit(r.Context(), id, campaign.Edit{
		Version:  *req.Version,
		Channels: *req.Channels,
		Drafts: content.Drafts{
			SMS:   content.SMS{Body: *req.SMSBody},
			Email: content.Email{Subject: *req.EmailSubject, Body: *req.EmailBody},
		},
	})
	if err != nil {
		h.writeError(w, r, "edit campaign", err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, h.campaignResponse(c))
}

func (h campaignHandler) preview(w http.ResponseWriter, r *http.Request) {
	id, ok := h.campaignID(w, r)
	if !ok {
		return
	}
	p, err := h.svc.Preview(r.Context(), id)
	if err != nil {
		h.writeError(w, r, "preview campaign", err)
		return
	}
	out := ContentPreviewResponse{
		Version:     p.Campaign.Version,
		ContentHash: p.Campaign.ContentHash,
		Validation:  validationResponse(p.Validation),
		Channels:    make([]ChannelPreview, len(p.Channels)),
	}
	for i, cp := range p.Channels {
		samples := make([]SamplePreview, len(cp.Samples))
		for j, s := range cp.Samples {
			fields := make([]RenderedField, len(s.Fields))
			for k, f := range s.Fields {
				fields[k] = RenderedField{Field: f.Path, Text: f.Text, Length: f.Runes, Limit: f.MaxRunes, OverLimit: f.OverLimit}
			}
			samples[j] = SamplePreview{
				UserID:    s.Recipient.UserID,
				FirstName: s.Recipient.Values[content.VarFirstName],
				City:      s.Recipient.Values[content.VarCity],
				Fields:    fields,
			}
		}
		longest := cp.Longest
		if longest == nil {
			longest = map[content.Variable]int{}
		}
		out.Channels[i] = ChannelPreview{Channel: cp.Channel, Recipients: cp.Recipients, LongestValues: longest, Samples: samples}
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

func (h campaignHandler) approve(w http.ResponseWriter, r *http.Request) {
	id, ok := h.campaignID(w, r)
	if !ok {
		return
	}
	var req ApproveRequest
	if !h.decode(w, r, MaxApproveBodyBytes, &req,
		"request body must be a JSON object with version, content_hash, and approved_by") {
		return
	}
	if req.Version == nil || req.ContentHash == nil || req.ApprovedBy == nil {
		writeCampaignError(w, http.StatusBadRequest, codeInvalidRequest,
			"version, content_hash, and approved_by are required", nil, nil)
		return
	}
	c, err := h.svc.Approve(r.Context(), id, campaign.Approval{
		Version: *req.Version, ContentHash: *req.ContentHash, ApprovedBy: *req.ApprovedBy,
	})
	if err != nil {
		h.writeError(w, r, "approve campaign", err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, h.campaignResponse(c))
}

// campaignID parses the {id} path parameter; anything but a UUID is 404.
func (h campaignHandler) campaignID(w http.ResponseWriter, r *http.Request) (campaign.ID, bool) {
	id, err := campaign.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		writeCampaignError(w, http.StatusNotFound, string(campaign.CodeNotFound), "campaign not found", nil, nil)
		return "", false
	}
	return id, true
}

// decode reads and strictly decodes a JSON body into dst. The body's
// content is validated by the domain, not here.
func (h campaignHandler) decode(w http.ResponseWriter, r *http.Request, limit int64, dst any, shape string) bool {
	body, status, msg := readJSONBody(w, r, limit)
	if status != 0 {
		writeCampaignError(w, status, codeInvalidRequest, msg, nil, nil)
		return false
	}
	if err := strictjson.Decode(body, dst); err != nil {
		writeCampaignError(w, http.StatusBadRequest, codeInvalidRequest, shape, nil, nil)
		return false
	}
	return true
}

func (h campaignHandler) campaignResponse(c campaign.Campaign) CampaignResponse {
	out := CampaignResponse{
		ID:     string(c.ID),
		Brief:  c.Brief,
		Status: c.Status,
		Audience: AudienceResponse{
			Rule:                c.Rule,
			Summary:             segment.Describe(c.Rule),
			Explanation:         c.Explanation,
			UnsupportedCriteria: c.UnsupportedCriteria,
			Counts:              c.Counts,
		},
		Channels:    nonNilChannels(c.Channels),
		Version:     c.Version,
		ContentHash: c.ContentHash,
		Generating:  c.Generating(h.now()),
		ApprovedAt:  optionalTime(c.ApprovedAt),
		CreatedAt:   c.CreatedAt,
		UpdatedAt:   c.UpdatedAt,
	}
	if out.Audience.UnsupportedCriteria == nil {
		out.Audience.UnsupportedCriteria = []string{}
	}
	if c.ApprovedBy != "" {
		out.ApprovedBy = &c.ApprovedBy
	}
	if c.HasDrafts() {
		out.Drafts = draftsBody(c.Drafts)
		v := validationResponse(c.Validate())
		out.Validation = &v
	}
	return out
}

func draftsBody(d content.Drafts) *DraftsBody {
	return &DraftsBody{SMSBody: d.SMS.Body, EmailSubject: d.Email.Subject, EmailBody: d.Email.Body}
}

func validationResponse(v campaign.Validation) ValidationResponse {
	errs := v.Errors
	if errs == nil {
		errs = []content.FieldError{}
	}
	return ValidationResponse{Valid: v.Valid, Errors: errs, WorstCaseLengths: v.WorstCase, Limits: fieldLimits}
}

func nonNilChannels(cs []content.Channel) []content.Channel {
	if cs == nil {
		return []content.Channel{}
	}
	return cs
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// writeError maps a service error to a response. Domain errors carry a
// client-safe message; anything else becomes a generic 500.
func (h campaignHandler) writeError(w http.ResponseWriter, r *http.Request, op string, err error) {
	status, body := h.failure(r.Context(), op, err)
	httpserver.WriteJSON(w, status, body)
}

// failure logs err and maps it to a status and client-safe body. It is
// shared by JSON responses and the draft stream's error event.
func (h campaignHandler) failure(ctx context.Context, op string, err error, attrs ...slog.Attr) (int, ErrorResponse) {
	attrs = append(attrs, slog.String("request_id", middleware.GetReqID(ctx)))
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
		// contains the brief or message content.
		h.logger.LogAttrs(ctx, level, op+" failed",
			append(attrs, slog.String("code", string(gerr.Code)), slog.Any("error", gerr.Err))...)
		return status, ErrorResponse{Error: gerr.Message, Code: string(gerr.Code), Details: gerr.Details, UnsupportedCriteria: gerr.UnsupportedCriteria}
	}
	if errors.Is(err, context.Canceled) {
		h.logger.LogAttrs(ctx, slog.LevelInfo, op+" cancelled by client", attrs...)
		return StatusClientClosedRequest, ErrorResponse{Error: "request cancelled", Code: "cancelled"}
	}
	h.logger.LogAttrs(ctx, slog.LevelError, op+" failed", append(attrs, slog.Any("error", err))...)
	return http.StatusInternalServerError, ErrorResponse{Error: "internal server error", Code: "internal"}
}

func writeCampaignError(w http.ResponseWriter, status int, code, msg string, details []content.FieldError, unsupported []string) {
	httpserver.WriteJSON(w, status, ErrorResponse{Error: msg, Code: code, Details: details, UnsupportedCriteria: unsupported})
}
