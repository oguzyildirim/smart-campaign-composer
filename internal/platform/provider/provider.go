// Package provider is the HTTP client for the local mock messaging
// provider (mock-provider/). There is no real SMS or email integration,
// and none may be added.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/delivery"
)

// MaxRetryAfter caps a parsed Retry-After, so a hostile or broken value
// cannot park a delivery for years. The retry policy caps it further.
const MaxRetryAfter = time.Hour

// maxResponseBytes bounds how much of a response is read.
const maxResponseBytes = 64 << 10

// Observer receives every provider request's outcome. codeClass is one
// of the CodeClass values; implementations may use it as a label.
type Observer interface {
	ProviderRequest(c content.Channel, codeClass string, d time.Duration)
}

// Code classes reported to the Observer: the response status class, 429
// on its own (it drives the rate-limit handling), or "error" when no
// response arrived (timeout, connection failure).
const (
	CodeClass2xx   = "2xx"
	CodeClass3xx   = "3xx"
	CodeClass429   = "429"
	CodeClass4xx   = "4xx"
	CodeClass5xx   = "5xx"
	CodeClassError = "error"
)

// Client sends messages to the provider. It implements delivery.Provider.
type Client struct {
	base     *url.URL
	http     *http.Client
	now      func() time.Time
	observer Observer
}

var _ delivery.Provider = (*Client)(nil)

// New returns a client for the provider at baseURL. timeout bounds each
// request, including reading the response.
func New(baseURL string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, errors.New("provider URL must be http(s)://host[:port] without credentials")
	}
	return &Client{base: u, http: &http.Client{Timeout: timeout}, now: time.Now}, nil
}

// WithObserver reports each request to o and returns c.
func (c *Client) WithObserver(o Observer) *Client {
	c.observer = o
	return c
}

func (c *Client) observe(ch content.Channel, codeClass string, start time.Time) {
	if c.observer != nil {
		c.observer.ProviderRequest(ch, codeClass, time.Since(start))
	}
}

// codeClass maps an HTTP status to its CodeClass.
func codeClass(status int) string {
	switch {
	case status == http.StatusTooManyRequests:
		return CodeClass429
	case status >= 500:
		return CodeClass5xx
	case status >= 400:
		return CodeClass4xx
	case status >= 300:
		return CodeClass3xx
	default:
		return CodeClass2xx
	}
}

type smsRequest struct {
	To   string `json:"to"`
	Body string `json:"body"`
}

type emailRequest struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type sendResponse struct {
	MessageID string `json:"message_id"`
}

// Send implements delivery.Provider. Failures are *delivery.ProviderError,
// except that ctx's own error is returned as is when ctx ended.
func (c *Client) Send(ctx context.Context, msg delivery.Message) (string, error) {
	var (
		path string
		body any
	)
	switch msg.Channel {
	case content.ChannelSMS:
		path, body = "/v1/sms", smsRequest{To: msg.To, Body: msg.Body}
	case content.ChannelEmail:
		path, body = "/v1/email", emailRequest{To: msg.To, Subject: msg.Subject, Body: msg.Body}
	default:
		return "", &delivery.ProviderError{Code: delivery.CodeRejected, Err: fmt.Errorf("unknown channel %q", msg.Channel)}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", &delivery.ProviderError{Code: delivery.CodeRejected, Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base.JoinPath(path).String(), bytes.NewReader(payload))
	if err != nil {
		return "", &delivery.ProviderError{Code: delivery.CodeRejected, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", string(msg.IdempotencyKey))

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.observe(msg.Channel, CodeClassError, start)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// Timeouts and connection errors: the message may or may not have
		// arrived, so the retry must reuse the idempotency key.
		return "", &delivery.ProviderError{Code: delivery.CodeTimeout, Transient: true, Err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		c.observe(msg.Channel, CodeClassError, start)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &delivery.ProviderError{Code: delivery.CodeTimeout, Transient: true, Err: err}
	}

	c.observe(msg.Channel, codeClass(resp.StatusCode), start)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var r sendResponse
		if err := json.Unmarshal(data, &r); err != nil || r.MessageID == "" || len(r.MessageID) > 128 {
			return "", &delivery.ProviderError{Code: delivery.CodeInvalidResponse, Transient: true,
				Err: errors.New("2xx response without a valid message_id")}
		}
		return r.MessageID, nil
	}
	code, transient := classify(resp.StatusCode)
	pe := &delivery.ProviderError{Code: code, Transient: transient, Err: fmt.Errorf("status %d", resp.StatusCode)}
	if transient {
		pe.RetryAfter, _ = ParseRetryAfter(resp.Header.Get("Retry-After"), c.now())
	}
	return "", pe
}

// classify maps a non-2xx status to an error code and whether retrying
// can help: 408, 429 and 5xx are transient, other 4xx are permanent.
func classify(status int) (code string, transient bool) {
	switch {
	case status == http.StatusRequestTimeout:
		return delivery.CodeTimeout, true
	case status == http.StatusTooManyRequests:
		return delivery.CodeRateLimited, true
	case status >= 500:
		return delivery.CodeUnavailable, true
	case status >= 400:
		return delivery.CodeRejected, false
	default:
		// 1xx/3xx are unexpected from this API; retry with the same key.
		return delivery.CodeInvalidResponse, true
	}
}

// ParseRetryAfter reads a Retry-After header: delay-seconds or an
// HTTP-date relative to now. ok is false for an absent or invalid value.
// Dates in the past yield 0; values are capped at MaxRetryAfter.
func ParseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if isDigits(v) {
		secs, err := strconv.ParseInt(v, 10, 64)
		if err != nil || secs > int64(MaxRetryAfter/time.Second) {
			return MaxRetryAfter, true // overflowing digit strings are huge, not invalid
		}
		return time.Duration(secs) * time.Second, true
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	return min(max(t.Sub(now), 0), MaxRetryAfter), true
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
