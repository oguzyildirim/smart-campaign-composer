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
	"github.com/oguzyildirim/smart-campaign-composer/internal/llm/mockllm"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/httpserver"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

const exampleBrief = "Son 30 günde sepetine ürün ekleyip alışveriş yapmayan İstanbul'daki kullanıcılara %30 indirim hatırlatması gönder. Samimi ama kısa olsun."

func newAIServer(t *testing.T, script ...mockllm.Step) (http.Handler, *stubCounter) {
	t.Helper()
	counter := &stubCounter{counts: segment.Counts{Total: 120, SMS: 80, Email: 95}}
	segments := segment.NewService(counter, func() time.Time { return time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC) })
	cfg := campaign.GeneratorConfig{SegmentTimeout: 50 * time.Millisecond, DraftsTimeout: 50 * time.Millisecond, MaxAttempts: 2}
	logger := slog.New(slog.DiscardHandler)
	gen := campaign.NewGenerator(mockllm.New(script...), segments, cfg, logger)
	r := httpserver.NewRouter(logger, 5*time.Second)
	MountAI(r, gen, logger)
	return r, counter
}

func postAI(h http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func briefBody(t *testing.T, brief string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"brief": brief})
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

func TestAISegment_OK(t *testing.T) {
	h, _ := newAIServer(t)
	rec := postAI(h, "/ai/segment", briefBody(t, exampleBrief))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	wantRule := `{"version":1,"conditions":[{"field":"city","op":"eq","value":"İstanbul"},` +
		`{"field":"added_to_cart_within_days","op":"eq","value":30},` +
		`{"field":"no_purchase_within_days","op":"eq","value":30}]}`
	if string(got["rule"]) != wantRule {
		t.Errorf("rule = %s", got["rule"])
	}
	if string(got["counts"]) != `{"total":120,"sms":80,"email":95}` {
		t.Errorf("counts = %s", got["counts"])
	}
	if string(got["unsupported_criteria"]) != `[]` {
		t.Errorf("unsupported_criteria = %s, want []", got["unsupported_criteria"])
	}
	if len(got["explanation"]) < 3 {
		t.Errorf("explanation = %s", got["explanation"])
	}
}

func TestAIDrafts_OK(t *testing.T) {
	h, _ := newAIServer(t)
	rec := postAI(h, "/ai/drafts", briefBody(t, exampleBrief))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	got := decodeBody[DraftsResponse](t, rec)
	if !strings.Contains(got.Drafts.SMS.Body, "{{first_name}}") || got.Drafts.Email.Subject == "" || got.Drafts.Email.Body == "" {
		t.Errorf("drafts = %+v", got.Drafts)
	}
	if got.WorstCaseLengths.SMSBody == 0 || got.WorstCaseLengths.SMSBody > 160 {
		t.Errorf("worst_case_lengths = %+v", got.WorstCaseLengths)
	}
}

func TestAI_BadRequests(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
		wantCode    string
	}{
		{"empty brief", `{"brief":""}`, "application/json", 400, "invalid_brief"},
		{"whitespace brief", `{"brief":"   "}`, "application/json", 400, "invalid_brief"},
		{"missing brief", `{}`, "application/json", 400, "invalid_brief"},
		{"brief too long", `{"brief":"` + strings.Repeat("ş", campaign.MaxBriefRunes+1) + `"}`, "application/json", 400, "invalid_brief"},
		{"brief not a string", `{"brief":42}`, "application/json", 400, "invalid_request"},
		{"unknown field", `{"brief":"x","approve":true}`, "application/json", 400, "invalid_request"},
		{"duplicate field", `{"brief":"x","brief":"y"}`, "application/json", 400, "invalid_request"},
		{"malformed json", `{"brief":`, "application/json", 400, "invalid_request"},
		{"body too large", `{"brief":"` + strings.Repeat("a", MaxBriefBodyBytes) + `"}`, "application/json", 400, "invalid_request"},
		{"wrong content type", `{"brief":"x"}`, "text/plain", 415, "invalid_request"},
	}
	for _, path := range []string{"/ai/segment", "/ai/drafts"} {
		for _, tt := range tests {
			t.Run(path+"/"+tt.name, func(t *testing.T) {
				h, counter := newAIServer(t)
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(tt.body))
				req.Header.Set("Content-Type", tt.contentType)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)

				if rec.Code != tt.wantStatus {
					t.Errorf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body)
				}
				if got := decodeBody[AIErrorResponse](t, rec); got.Code != tt.wantCode || got.Error == "" {
					t.Errorf("body = %+v, want code %s", got, tt.wantCode)
				}
				if counter.calls != 0 {
					t.Error("database reached for a bad request")
				}
			})
		}
	}
}

func TestAI_ErrorMapping(t *testing.T) {
	sqlOutput := `{"rule":{"version":1,"conditions":[{"field":"city","op":"eq","value":"x'; DROP TABLE users;--"}]},"unsupported_criteria":[],"explanation":"SECRET-MODEL-TEXT"}`
	tests := []struct {
		name       string
		path       string
		brief      string
		script     []mockllm.Step
		wantStatus int
		wantCode   string
	}{
		{"invalid output", "/ai/segment", exampleBrief, []mockllm.Step{{Output: sqlOutput}, {Output: sqlOutput}}, 502, "invalid_output"},
		{"invalid drafts", "/ai/drafts", exampleBrief, []mockllm.Step{{Fault: mockllm.FaultUnknownVariable}, {Fault: mockllm.FaultOverlongSMS}}, 502, "invalid_output"},
		{"unsupported brief", "/ai/segment", "Send a coupon to users older than 30", nil, 422, "unsupported_brief"},
		{"refused", "/ai/segment", exampleBrief, []mockllm.Step{{Fault: mockllm.FaultRefusal}}, 422, "refused"},
		{"refused drafts", "/ai/drafts", exampleBrief, []mockllm.Step{{Fault: mockllm.FaultRefusal}}, 422, "refused"},
		{"timeout", "/ai/segment", exampleBrief, []mockllm.Step{{Fault: mockllm.FaultHang}}, 504, "timeout"},
		{"timeout drafts", "/ai/drafts", exampleBrief, []mockllm.Step{{Fault: mockllm.FaultHang}}, 504, "timeout"},
		{"upstream", "/ai/segment", exampleBrief, []mockllm.Step{{Fault: mockllm.FaultAPIError}}, 503, "upstream_unavailable"},
		{"upstream drafts", "/ai/drafts", exampleBrief, []mockllm.Step{{Fault: mockllm.FaultAPIError}}, 503, "upstream_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newAIServer(t, tt.script...)
			rec := postAI(h, tt.path, briefBody(t, tt.brief))
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body)
			}
			got := decodeBody[AIErrorResponse](t, rec)
			if got.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", got.Code, tt.wantCode)
			}
			for _, leak := range []string{"DROP", "SECRET-MODEL-TEXT", "mock", "529", "llm:", "{{phone}}"} {
				if strings.Contains(rec.Body.String(), leak) {
					t.Errorf("response leaks %q: %s", leak, rec.Body)
				}
			}
		})
	}
}

func TestAI_UnsupportedCriteriaReturned(t *testing.T) {
	h, _ := newAIServer(t)
	rec := postAI(h, "/ai/segment", briefBody(t, "30 yaşından büyük kadın kullanıcılar"))
	got := decodeBody[AIErrorResponse](t, rec)
	if strings.Join(got.UnsupportedCriteria, ",") != "age,gender" {
		t.Errorf("unsupported_criteria = %q, want [age gender]", got.UnsupportedCriteria)
	}
}

func TestAI_ClientGone(t *testing.T) {
	h, _ := newAIServer(t, mockllm.Step{Fault: mockllm.FaultHang})
	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/ai/segment", strings.NewReader(briefBody(t, exampleBrief)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	cancel()
	h.ServeHTTP(rec, req)
	if rec.Code != StatusClientClosedRequest {
		t.Errorf("status = %d, want %d", rec.Code, StatusClientClosedRequest)
	}
}
