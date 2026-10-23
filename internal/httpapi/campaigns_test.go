package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign/campaigntest"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/llm/mockllm"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/httpserver"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

const exampleBrief = "Son 30 günde sepetine ürün ekleyip alışveriş yapmayan İstanbul'daki kullanıcılara %30 indirim hatırlatması gönder. Samimi ama kısa olsun."

var testNow = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

type fixedCounter segment.Counts

func (c fixedCounter) CountAudience(context.Context, segment.Predicate) (segment.Counts, error) {
	return segment.Counts(c), nil
}

type apiFixture struct {
	handler http.Handler
	svc     *campaign.Service
	store   *campaigntest.Store
	llm     *mockllm.Client
}

type fixtureOptions struct {
	script      []mockllm.Step
	streamDelay time.Duration
	heartbeat   time.Duration
	callTimeout time.Duration
}

func newAPI(t *testing.T, opts fixtureOptions) apiFixture {
	t.Helper()
	if opts.callTimeout == 0 {
		opts.callTimeout = 5 * time.Second
	}
	f := apiFixture{llm: mockllm.New(opts.script...).WithStreamDelay(opts.streamDelay)}
	audience := campaigntest.NewAudience(map[content.Channel]campaign.ChannelAudience{
		content.ChannelSMS: {Recipients: 80, Longest: map[content.Variable]int{content.VarFirstName: 10, content.VarCity: 8},
			Samples: []campaign.Recipient{campaigntest.Recipient(7, "Ayşe", "İstanbul")}},
		content.ChannelEmail: {Recipients: 95, Longest: map[content.Variable]int{content.VarFirstName: 10, content.VarCity: 8},
			Samples: []campaign.Recipient{campaigntest.Recipient(8, "Can", "İstanbul")}},
	})
	f.store = campaigntest.NewStore(audience)
	logger := slog.New(slog.DiscardHandler)
	now := func() time.Time { return testNow }
	gen := campaign.NewGenerator(f.llm, segment.NewService(fixedCounter{Total: 120, SMS: 80, Email: 95}, now),
		campaign.GeneratorConfig{SegmentTimeout: opts.callTimeout, DraftsTimeout: opts.callTimeout, MaxAttempts: 2}, logger)
	f.svc = campaign.NewService(gen, f.store, audience,
		campaign.ServiceConfig{GenerationTimeout: 10 * time.Second, PreviewSamples: 3}, now, logger)
	r := httpserver.NewRouter(logger, 0)
	MountCampaigns(r, CampaignRoutes{
		Service: f.svc, Logger: logger, RequestTimeout: 5 * time.Second, CreateTimeout: 5 * time.Second,
		Heartbeat: opts.heartbeat, Now: now,
	})
	f.handler = r
	return f
}

func (f apiFixture) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func jsonBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func requireError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) ErrorResponse {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, status, rec.Body)
	}
	body := decodeBody[ErrorResponse](t, rec)
	if body.Code != code || body.Error == "" {
		t.Fatalf("error body = %+v, want code %s", body, code)
	}
	return body
}

// ready creates a campaign and generates its drafts through the service.
func (f apiFixture) ready(t *testing.T) campaign.Campaign {
	t.Helper()
	c, err := f.svc.Create(t.Context(), exampleBrief)
	if err != nil {
		t.Fatal(err)
	}
	g, err := f.svc.BeginGeneration(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c, err = g.Run(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCreateCampaign(t *testing.T) {
	f := newAPI(t, fixtureOptions{})
	rec := f.do(http.MethodPost, "/campaigns", jsonBody(t, map[string]string{"brief": exampleBrief}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"status": `"audience_ready"`, "drafts": "null", "validation": "null", "version": "1",
		"channels": `["sms","email"]`, "generating": "false", "approved_by": "null", "approved_at": "null",
	} {
		if string(raw[key]) != want {
			t.Errorf("%s = %s, want %s", key, raw[key], want)
		}
	}
	got := decodeBody[CampaignResponse](t, rec)
	if got.Audience.Counts != (segment.Counts{Total: 120, SMS: 80, Email: 95}) ||
		strings.Join(got.Audience.Summary, "; ") != "City is İstanbul; Added to cart in the last 30 days; No purchase in the last 30 days" ||
		got.Audience.UnsupportedCriteria == nil || len(got.ContentHash) != 64 {
		t.Errorf("campaign = %+v", got)
	}
}

func TestCreateCampaign_Errors(t *testing.T) {
	tests := []struct {
		name   string
		script []mockllm.Step
		body   string
		ctype  string
		status int
		code   string
	}{
		{name: "empty brief", body: `{"brief":"   "}`, status: http.StatusBadRequest, code: "invalid_brief"},
		{name: "brief too long", body: `{"brief":"` + strings.Repeat("a", campaign.MaxBriefRunes+1) + `"}`,
			status: http.StatusBadRequest, code: "invalid_brief"},
		{name: "unknown field", body: `{"brief":"x","approve":true}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "malformed json", body: `{"brief":`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "body too large", body: `{"brief":"` + strings.Repeat("a", MaxBriefBodyBytes) + `"}`,
			status: http.StatusBadRequest, code: "invalid_request"},
		{name: "wrong content type", body: `{"brief":"x"}`, ctype: "text/plain", status: http.StatusUnsupportedMediaType, code: "invalid_request"},
		{name: "unsupported brief", body: `{"brief":"Send to users older than 30"}`,
			status: http.StatusUnprocessableEntity, code: "unsupported_brief"},
		{name: "refused", script: []mockllm.Step{{Fault: mockllm.FaultRefusal}}, body: `{"brief":"x istanbul"}`,
			status: http.StatusUnprocessableEntity, code: "refused"},
		{name: "invalid output", script: []mockllm.Step{{Fault: mockllm.FaultMalformedJSON}, {Fault: mockllm.FaultMalformedJSON}},
			body: `{"brief":"x istanbul"}`, status: http.StatusBadGateway, code: "invalid_output"},
		{name: "upstream unavailable", script: []mockllm.Step{{Fault: mockllm.FaultAPIError}}, body: `{"brief":"x istanbul"}`,
			status: http.StatusServiceUnavailable, code: "upstream_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newAPI(t, fixtureOptions{script: tt.script})
			req := httptest.NewRequest(http.MethodPost, "/campaigns", strings.NewReader(tt.body))
			ctype := tt.ctype
			if ctype == "" {
				ctype = "application/json"
			}
			req.Header.Set("Content-Type", ctype)
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)
			body := requireError(t, rec, tt.status, tt.code)
			if tt.code == "unsupported_brief" && len(body.UnsupportedCriteria) == 0 {
				t.Error("unsupported_criteria missing")
			}
			if list, _ := f.svc.List(t.Context()); len(list) != 0 {
				t.Errorf("a failed create stored %d campaigns", len(list))
			}
		})
	}
}

func TestCreateCampaign_Timeout(t *testing.T) {
	f := newAPI(t, fixtureOptions{script: []mockllm.Step{{Fault: mockllm.FaultHang}}, callTimeout: 20 * time.Millisecond})
	rec := f.do(http.MethodPost, "/campaigns", `{"brief":"istanbul"}`)
	requireError(t, rec, http.StatusGatewayTimeout, "timeout")
}

func TestGetAndListCampaigns(t *testing.T) {
	f := newAPI(t, fixtureOptions{})
	c := f.ready(t)

	rec := f.do(http.MethodGet, "/campaigns/"+string(c.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	got := decodeBody[CampaignResponse](t, rec)
	if got.Status != campaign.StatusDraftsReady || got.Drafts == nil || got.Drafts.SMSBody != c.Drafts.SMS.Body ||
		got.Validation == nil || !got.Validation.Valid || got.Validation.WorstCaseLengths.SMSBody == 0 ||
		got.Validation.Limits != (content.WorstCase{SMSBody: 160, EmailSubject: 80, EmailBody: 2000}) {
		t.Errorf("campaign = %+v", got)
	}

	list := decodeBody[ListResponse](t, f.do(http.MethodGet, "/campaigns", ""))
	if len(list.Campaigns) != 1 || list.Campaigns[0].ID != string(c.ID) || list.Campaigns[0].Status != campaign.StatusDraftsReady {
		t.Errorf("list = %+v", list)
	}

	requireError(t, f.do(http.MethodGet, "/campaigns/00000000-0000-4000-8000-999999999999", ""), http.StatusNotFound, "not_found")
	requireError(t, f.do(http.MethodGet, "/campaigns/not-a-uuid", ""), http.StatusNotFound, "not_found")
	requireError(t, f.do(http.MethodGet, "/campaigns/1'%20OR%20'1'='1", ""), http.StatusNotFound, "not_found")
}

func editBody(t *testing.T, c campaign.Campaign, mutate func(map[string]any)) string {
	t.Helper()
	body := map[string]any{
		"version": c.Version, "channels": []string{"sms", "email"},
		"sms_body": c.Drafts.SMS.Body, "email_subject": c.Drafts.Email.Subject, "email_body": c.Drafts.Email.Body,
	}
	if mutate != nil {
		mutate(body)
	}
	return jsonBody(t, body)
}

func TestEditCampaign(t *testing.T) {
	f := newAPI(t, fixtureOptions{})
	c := f.ready(t)
	path := "/campaigns/" + string(c.ID)

	rec := f.do(http.MethodPatch, path, editBody(t, c, func(b map[string]any) {
		b["channels"] = []string{"email"}
		b["sms_body"] = "Hi {{phone}}" // stored, but only checked when sms is selected
		b["email_subject"] = strings.Repeat("ş", 81)
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	got := decodeBody[CampaignResponse](t, rec)
	if got.Version != c.Version+1 || got.ContentHash == c.ContentHash || got.Validation.Valid ||
		len(got.Validation.Errors) != 1 || got.Validation.Errors[0].Path != "email.subject" ||
		strings.Contains(got.Validation.Errors[0].Message, "ş") {
		t.Errorf("edited = %+v", got)
	}

	requireError(t, f.do(http.MethodPatch, path, editBody(t, c, nil)), http.StatusConflict, "version_conflict")

	missing := requireError(t, f.do(http.MethodPatch, path, `{"version":3,"sms_body":"x"}`), http.StatusBadRequest, "invalid_request")
	if len(missing.Details) != 3 {
		t.Errorf("details = %+v, want channels, email_subject, email_body", missing.Details)
	}
	bad := requireError(t, f.do(http.MethodPatch, path, editBody(t, c, func(b map[string]any) {
		b["version"] = got.Version
		b["channels"] = []string{"push"}
	})), http.StatusBadRequest, "invalid_request")
	if len(bad.Details) != 1 || bad.Details[0].Path != "channels" {
		t.Errorf("details = %+v", bad.Details)
	}
	requireError(t, f.do(http.MethodPatch, path, `{"version":1,"channels":[],"sms_body":"","email_subject":"","email_body":"","status":"approved"}`),
		http.StatusBadRequest, "invalid_request")
}

func TestEditCampaign_WrongState(t *testing.T) {
	f := newAPI(t, fixtureOptions{})
	c, err := f.svc.Create(t.Context(), exampleBrief)
	if err != nil {
		t.Fatal(err)
	}
	requireError(t, f.do(http.MethodPatch, "/campaigns/"+string(c.ID), editBody(t, c, nil)), http.StatusConflict, "invalid_state")
}

func TestPreviewCampaign(t *testing.T) {
	f := newAPI(t, fixtureOptions{})
	c := f.ready(t)
	rec := f.do(http.MethodGet, "/campaigns/"+string(c.ID)+"/preview", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	p := decodeBody[ContentPreviewResponse](t, rec)
	if p.Version != c.Version || p.ContentHash != c.ContentHash || !p.Validation.Valid || len(p.Channels) != 2 {
		t.Fatalf("preview = %+v", p)
	}
	sms := p.Channels[0]
	want := strings.ReplaceAll(c.Drafts.SMS.Body, "{{first_name}}", "Ayşe")
	if sms.Recipients != 80 || sms.LongestValues[content.VarFirstName] != 10 || len(sms.Samples) != 1 ||
		sms.Samples[0].FirstName != "Ayşe" || sms.Samples[0].Fields[0].Text != want ||
		sms.Samples[0].Fields[0].Length != len([]rune(want)) || sms.Samples[0].Fields[0].Limit != 160 {
		t.Errorf("sms preview = %+v", sms)
	}
	if strings.Contains(rec.Body.String(), "example.com") || strings.Contains(rec.Body.String(), "+9995") {
		t.Error("preview exposes contact details")
	}

	created, err := f.svc.Create(t.Context(), exampleBrief)
	if err != nil {
		t.Fatal(err)
	}
	requireError(t, f.do(http.MethodGet, "/campaigns/"+string(created.ID)+"/preview", ""), http.StatusConflict, "invalid_state")
}

func approveBody(t *testing.T, c campaign.Campaign, mutate func(map[string]any)) string {
	t.Helper()
	body := map[string]any{"version": c.Version, "content_hash": c.ContentHash, "approved_by": "Deniz Operator"}
	if mutate != nil {
		mutate(body)
	}
	return jsonBody(t, body)
}

func TestApproveCampaign(t *testing.T) {
	f := newAPI(t, fixtureOptions{})
	c := f.ready(t)
	path := "/campaigns/" + string(c.ID) + "/approve"

	requireError(t, f.do(http.MethodPost, path, approveBody(t, c, func(b map[string]any) { b["version"] = c.Version - 1 })),
		http.StatusConflict, "version_conflict")
	requireError(t, f.do(http.MethodPost, path, approveBody(t, c, func(b map[string]any) { b["content_hash"] = strings.Repeat("0", 64) })),
		http.StatusConflict, "content_changed")
	requireError(t, f.do(http.MethodPost, path, `{"version":1,"content_hash":"x"}`), http.StatusBadRequest, "invalid_request")
	requireError(t, f.do(http.MethodPost, path, approveBody(t, c, func(b map[string]any) { b["approved_by"] = "" })),
		http.StatusBadRequest, "invalid_request")

	rec := f.do(http.MethodPost, path, approveBody(t, c, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	got := decodeBody[CampaignResponse](t, rec)
	if got.Status != campaign.StatusApproved || got.ApprovedBy == nil || *got.ApprovedBy != "Deniz Operator" ||
		got.ApprovedAt == nil || !got.ApprovedAt.Equal(testNow) {
		t.Errorf("approved = %+v", got)
	}

	// Idempotent: the same request again returns the same approval.
	again := f.do(http.MethodPost, path, approveBody(t, c, nil))
	if again.Code != http.StatusOK || decodeBody[CampaignResponse](t, again).Version != got.Version {
		t.Errorf("re-approve = %d %s", again.Code, again.Body)
	}
	// Approved campaigns are immutable.
	requireError(t, f.do(http.MethodPatch, "/campaigns/"+string(c.ID), editBody(t, campaign.Campaign{Version: got.Version, Drafts: c.Drafts}, nil)),
		http.StatusConflict, "invalid_state")
}

func TestApproveCampaign_InvalidContent(t *testing.T) {
	f := newAPI(t, fixtureOptions{})
	c := f.ready(t)
	rec := f.do(http.MethodPatch, "/campaigns/"+string(c.ID), editBody(t, c, func(b map[string]any) { b["sms_body"] = "Hi {{phone}}" }))
	edited := decodeBody[CampaignResponse](t, rec)
	body := requireError(t, f.do(http.MethodPost, "/campaigns/"+string(c.ID)+"/approve", approveBody(t,
		campaign.Campaign{Version: edited.Version, ContentHash: edited.ContentHash}, nil)),
		http.StatusUnprocessableEntity, "invalid_content")
	if len(body.Details) != 1 || body.Details[0].Path != "sms.body" {
		t.Errorf("details = %+v", body.Details)
	}
}
