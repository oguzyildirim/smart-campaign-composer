package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/delivery"
)

var refNow = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{"seconds", "7", 7 * time.Second, true},
		{"zero", "0", 0, true},
		{"surrounding spaces", " 3 ", 3 * time.Second, true},
		{"http date", refNow.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second, true},
		{"date in the past", refNow.Add(-time.Minute).Format(http.TimeFormat), 0, true},
		{"huge seconds capped", "99999999999999999", MaxRetryAfter, true},
		{"far future date capped", refNow.AddDate(5, 0, 0).Format(http.TimeFormat), MaxRetryAfter, true},
		{"empty", "", 0, false},
		{"negative", "-5", 0, false},
		{"fraction", "1.5", 0, false},
		{"garbage", "soon", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseRetryAfter(tt.value, refNow)
			if got != tt.want || ok != tt.ok {
				t.Errorf("ParseRetryAfter(%q) = %v, %v; want %v, %v", tt.value, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		status    int
		code      string
		transient bool
	}{
		{http.StatusRequestTimeout, delivery.CodeTimeout, true},
		{http.StatusTooManyRequests, delivery.CodeRateLimited, true},
		{http.StatusInternalServerError, delivery.CodeUnavailable, true},
		{http.StatusBadGateway, delivery.CodeUnavailable, true},
		{http.StatusServiceUnavailable, delivery.CodeUnavailable, true},
		{http.StatusGatewayTimeout, delivery.CodeUnavailable, true},
		{http.StatusBadRequest, delivery.CodeRejected, false},
		{http.StatusUnauthorized, delivery.CodeRejected, false},
		{http.StatusNotFound, delivery.CodeRejected, false},
		{http.StatusConflict, delivery.CodeRejected, false},
		{http.StatusUnprocessableEntity, delivery.CodeRejected, false},
		{http.StatusMovedPermanently, delivery.CodeInvalidResponse, true},
	}
	for _, tt := range tests {
		code, transient := classify(tt.status)
		if code != tt.code || transient != tt.transient {
			t.Errorf("classify(%d) = %s, %v; want %s, %v", tt.status, code, transient, tt.code, tt.transient)
		}
	}
}

func newClient(t *testing.T, h http.HandlerFunc, timeout time.Duration) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, timeout)
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return refNow }
	return c
}

var smsMsg = delivery.Message{
	IdempotencyKey: "0199f0a0-0000-7000-8000-000000000001", Channel: content.ChannelSMS,
	To: "+999500000001", Body: "Merhaba Ayşe",
}

func TestSend_Success(t *testing.T) {
	var gotPath, gotKey, gotType string
	var gotBody map[string]any
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey, gotType = r.URL.Path, r.Header.Get("Idempotency-Key"), r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, `{"message_id":"msg_123","status":"delivered","duplicate":false}`)
	}, time.Second)

	id, err := c.Send(t.Context(), smsMsg)
	if err != nil || id != "msg_123" {
		t.Fatalf("Send() = %q, %v", id, err)
	}
	if gotPath != "/v1/sms" || gotKey != string(smsMsg.IdempotencyKey) || gotType != "application/json" {
		t.Errorf("request path=%s key=%s type=%s", gotPath, gotKey, gotType)
	}
	if len(gotBody) != 2 || gotBody["to"] != smsMsg.To || gotBody["body"] != smsMsg.Body {
		t.Errorf("sms body = %v", gotBody)
	}

	email := smsMsg
	email.Channel, email.To, email.Subject = content.ChannelEmail, "ayse@example.com", "Konu"
	if _, err := c.Send(t.Context(), email); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/email" || gotBody["subject"] != "Konu" || gotBody["to"] != "ayse@example.com" {
		t.Errorf("email request path=%s body=%v", gotPath, gotBody)
	}
}

func TestSend_Errors(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		code       string
		transient  bool
		retryAfter time.Duration
	}{
		{"429 with Retry-After", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
		}, delivery.CodeRateLimited, true, 3 * time.Second},
		{"503 with an HTTP date", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", refNow.Add(20*time.Second).Format(http.TimeFormat))
			w.WriteHeader(http.StatusServiceUnavailable)
		}, delivery.CodeUnavailable, true, 20 * time.Second},
		{"500", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			delivery.CodeUnavailable, true, 0},
		{"422 is permanent", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnprocessableEntity) },
			delivery.CodeRejected, false, 0},
		{"timeout", func(_ http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body) // lets the server notice the client hanging up
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		}, delivery.CodeTimeout, true, 0},
		{"2xx with a malformed body", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"message_id":`) },
			delivery.CodeInvalidResponse, true, 0},
		{"2xx without a message id", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"status":"delivered"}`) },
			delivery.CodeInvalidResponse, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, tt.handler, 100*time.Millisecond)
			_, err := c.Send(t.Context(), smsMsg)
			pe, ok := errors.AsType[*delivery.ProviderError](err)
			if !ok {
				t.Fatalf("Send() error = %v, want *ProviderError", err)
			}
			if pe.Code != tt.code || pe.Transient != tt.transient || pe.RetryAfter != tt.retryAfter {
				t.Errorf("error = %+v, want code %s transient %v retry-after %v", pe, tt.code, tt.transient, tt.retryAfter)
			}
		})
	}
}

func TestSend_ConnectionRefusedIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c, err := New(url, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Send(t.Context(), smsMsg)
	if pe, ok := errors.AsType[*delivery.ProviderError](err); !ok || !pe.Transient || pe.Code != delivery.CodeTimeout {
		t.Fatalf("Send() error = %v, want transient %s", err, delivery.CodeTimeout)
	}
}

func TestSend_ParentCancellationIsNotAProviderError(t *testing.T) {
	c := newClient(t, func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}, time.Minute)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	time.AfterFunc(20*time.Millisecond, cancel)
	if _, err := c.Send(ctx, smsMsg); !errors.Is(err, context.Canceled) {
		t.Fatalf("Send() error = %v, want context.Canceled", err)
	}
}

func TestNew_ValidatesURL(t *testing.T) {
	for _, bad := range []string{"", "mock-provider:9090", "ftp://x", "http://", "http://user:pw@host"} {
		if _, err := New(bad, time.Second); err == nil {
			t.Errorf("New(%q) accepted", bad)
		}
	}
}
