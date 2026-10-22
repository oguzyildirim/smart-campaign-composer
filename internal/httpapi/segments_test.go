package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/httpserver"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

const exampleRule = `{"version":1,"conditions":[
  {"field":"city","op":"eq","value":"Istanbul"},
  {"field":"added_to_cart_within_days","op":"eq","value":30},
  {"field":"no_purchase_within_days","op":"eq","value":30}
]}`

// stubCounter records whether the database layer would have been reached.
type stubCounter struct {
	calls  int
	counts segment.Counts
	err    error
}

func (s *stubCounter) CountAudience(context.Context, segment.Predicate) (segment.Counts, error) {
	s.calls++
	return s.counts, s.err
}

func newServer(t *testing.T, counter *stubCounter, logs *bytes.Buffer) http.Handler {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	if logs != nil {
		logger = slog.New(slog.NewJSONHandler(logs, nil))
	}
	svc := segment.NewService(counter, func() time.Time { return time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC) })
	r := httpserver.NewRouter(logger, 5*time.Second)
	MountSegments(r, svc, logger)
	return r
}

func post(h http.Handler, body, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/segments/preview", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPreview_OK(t *testing.T) {
	counter := &stubCounter{counts: segment.Counts{Total: 120, SMS: 80, Email: 95}}
	rec := post(newServer(t, counter, nil), exampleRule, "application/json; charset=utf-8")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	want := `{"rule":{"version":1,"conditions":[` +
		`{"field":"city","op":"eq","value":"Istanbul"},` +
		`{"field":"added_to_cart_within_days","op":"eq","value":30},` +
		`{"field":"no_purchase_within_days","op":"eq","value":30}]},` +
		`"counts":{"total":120,"sms":80,"email":95}}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
}

func TestPreview_ValidationErrors(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantPath string
	}{
		{"unknown JSON field", `{"version":1,"conditions":[{"field":"city","op":"eq","value":"A"}],"extra":true}`, "extra"},
		{"unknown condition field", `{"version":1,"conditions":[{"field":"city","op":"eq","value":"A","x":1}]}`, "conditions[0].x"},
		{"unknown segment field", `{"version":1,"conditions":[{"field":"password","op":"eq","value":"A"}]}`, "conditions[0].field"},
		{"unknown operator", `{"version":1,"conditions":[{"field":"city","op":"regex","value":".*"}]}`, "conditions[0].op"},
		{"wrong type", `{"version":1,"conditions":[{"field":"order_count","op":"gte","value":"5"}]}`, "conditions[0].value"},
		{"out of range", `{"version":1,"conditions":[{"field":"added_to_cart_within_days","op":"eq","value":1000}]}`, "conditions[0].value"},
		{"duplicate field", `{"version":1,"conditions":[{"field":"locale","op":"eq","value":"tr"},{"field":"locale","op":"eq","value":"en"}]}`,
			"conditions[1].field"},
		{"sql injection", `{"version":1,"conditions":[{"field":"city","op":"eq","value":"Istanbul'; DROP TABLE users;--"}]}`,
			"conditions[0].value"},
		{"malformed JSON", `{"version":1,`, "$"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			counter := &stubCounter{}
			rec := post(newServer(t, counter, nil), tt.body, "application/json")

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
			}
			var resp ValidationErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode body: %v (%s)", err, rec.Body)
			}
			if resp.Error != "invalid segment rule" || len(resp.Details) == 0 {
				t.Fatalf("body = %+v", resp)
			}
			found := false
			for _, d := range resp.Details {
				found = found || d.Path == tt.wantPath
			}
			if !found {
				t.Errorf("details = %+v, want path %q", resp.Details, tt.wantPath)
			}
			if counter.calls != 0 {
				t.Error("invalid rule reached the audience counter")
			}
		})
	}
}

func TestPreview_BodyTooLarge(t *testing.T) {
	counter := &stubCounter{}
	// Valid JSON that is only too large: padding inside a string value.
	body := `{"version":1,"conditions":[{"field":"city","op":"eq","value":"` +
		strings.Repeat("a", MaxSegmentBodyBytes) + `"}]}`
	rec := post(newServer(t, counter, nil), body, "application/json")

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "request body too large") {
		t.Errorf("got %d %s, want 400 request body too large", rec.Code, rec.Body)
	}
	if counter.calls != 0 {
		t.Error("oversized body reached the audience counter")
	}
}

func TestPreview_RequiresJSONContentType(t *testing.T) {
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		rec := post(newServer(t, &stubCounter{}, nil), exampleRule, ct)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q: status = %d, want 415", ct, rec.Code)
		}
	}
}

func TestPreview_MethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(t, &stubCounter{}, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/segments/preview", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestPreview_InternalErrorHidesDetails(t *testing.T) {
	var logs bytes.Buffer
	counter := &stubCounter{err: errors.New(`pq: relation "users" does not exist at 10.0.0.5:5432`)}
	rec := post(newServer(t, counter, &logs), exampleRule, "application/json")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "relation") || strings.Contains(body, "10.0.0.5") {
		t.Errorf("response leaks internal error: %s", body)
	}
	if !strings.Contains(logs.String(), "segment preview failed") || !strings.Contains(logs.String(), "request_id") {
		t.Errorf("failure not logged with request id: %s", logs.String())
	}
}
