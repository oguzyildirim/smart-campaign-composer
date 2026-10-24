// Command smoke runs the example campaign end to end against a running
// stack (docker compose up) and checks the delivery guarantees at the
// mock provider: create, stream drafts, approve, wait for completion, then
// compare the campaign's progress with the provider's statistics. It exits
// non-zero on the first failed check. Run it with `make smoke`.
//
// It talks only to the local API and mock provider; it reads no secrets.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/delivery"
	"github.com/oguzyildirim/smart-campaign-composer/internal/httpapi"
	"github.com/oguzyildirim/smart-campaign-composer/internal/mockprovider"
)

const exampleBrief = "Son 30 günde sepetine ürün ekleyip alışveriş yapmayan İstanbul'daki kullanıcılara %30 indirim hatırlatması gönder. Samimi ama kısa olsun."

type options struct {
	api, provider string
	timeout       time.Duration
	maxRate       int
}

func main() {
	var o options
	flag.StringVar(&o.api, "api", "http://127.0.0.1:8080", "API base URL")
	flag.StringVar(&o.provider, "provider", "http://127.0.0.1:9090", "mock provider base URL")
	flag.DurationVar(&o.timeout, "timeout", 5*time.Minute, "overall time limit")
	flag.IntVar(&o.maxRate, "max-rate", 100, "maximum provider requests per channel and second")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	err := run(ctx, o)
	cancel()
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "SMOKE FAILED:", err)
		os.Exit(1)
	}
	fmt.Println("SMOKE PASSED")
}

func run(ctx context.Context, o options) error {
	c := client{http: &http.Client{}}
	api := strings.TrimSuffix(o.api, "/")
	prov := strings.TrimSuffix(o.provider, "/")

	for _, u := range []string{api + "/readyz", prov + "/readyz"} {
		if err := c.waitReady(ctx, u); err != nil {
			return err
		}
	}
	before, err := getJSON[mockprovider.Stats](ctx, c, prov+"/v1/stats")
	if err != nil {
		return err
	}
	start := time.Now()

	created, err := send[httpapi.CampaignResponse](ctx, c, http.MethodPost, api+"/api/v1/campaigns", http.StatusCreated,
		map[string]string{"brief": exampleBrief})
	if err != nil {
		return fmt.Errorf("create campaign: %w", err)
	}
	logf("created campaign %s: %d recipients (sms %d, email %d)", created.ID,
		created.Audience.Counts.Total, created.Audience.Counts.SMS, created.Audience.Counts.Email)
	if created.Status != campaign.StatusAudienceReady || created.Audience.Counts.Total == 0 {
		return fmt.Errorf("created campaign: status %s with %d recipients, want audience_ready with recipients",
			created.Status, created.Audience.Counts.Total)
	}

	deltas, err := c.streamDrafts(ctx, api+"/api/v1/campaigns/"+created.ID+"/drafts")
	if err != nil {
		return fmt.Errorf("generate drafts: %w", err)
	}
	ready, err := getJSON[httpapi.CampaignResponse](ctx, c, api+"/api/v1/campaigns/"+created.ID)
	if err != nil {
		return err
	}
	if ready.Status != campaign.StatusDraftsReady || ready.Validation == nil || !ready.Validation.Valid {
		return fmt.Errorf("after generation: status %s, validation %+v; want valid drafts_ready", ready.Status, ready.Validation)
	}
	logf("drafts streamed in %d deltas; content valid", deltas)

	approved, err := send[httpapi.CampaignResponse](ctx, c, http.MethodPost, api+"/api/v1/campaigns/"+created.ID+"/approve", http.StatusOK,
		map[string]any{"version": ready.Version, "content_hash": ready.ContentHash, "approved_by": "smoke test"})
	if err != nil {
		return fmt.Errorf("approve: %w", err)
	}
	logf("approved (status %s); waiting for delivery", approved.Status)

	progress, err := c.waitCompleted(ctx, api+"/api/v1/campaigns/"+created.ID+"/progress")
	if err != nil {
		return err
	}
	logf("completed in %s: %d sent, %d failed of %d (failures %v)", time.Since(start).Round(time.Second),
		progress.Totals.Sent, progress.Totals.Failed, progress.Totals.Total, progress.Failures)

	after, err := getJSON[mockprovider.Stats](ctx, c, prov+"/v1/stats")
	if err != nil {
		return err
	}
	messages, err := getJSON[struct {
		Messages []mockprovider.Delivered `json:"messages"`
	}](ctx, c, prov+"/v1/messages?limit=1000")
	if err != nil {
		return err
	}
	return check(o, progress, before, after, messages.Messages, start)
}

// check compares the campaign's outcome with the provider's view.
func check(o options, p httpapi.ProgressResponse, before, after mockprovider.Stats, msgs []mockprovider.Delivered, start time.Time) error {
	var problems []string
	fail := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	t := p.Totals
	if t.Total == 0 || t.Sent+t.Failed != t.Total {
		fail("deliveries: %d sent + %d failed of %d; want every delivery terminal", t.Sent, t.Failed, t.Total)
	}
	exhausted := int64(0)
	for _, f := range p.Failures {
		switch f.Code {
		case delivery.CodeRetriesExhausted:
			exhausted = f.Count
		case delivery.CodeRejected, delivery.CodeConsentRevoked:
		default:
			fail("%d deliveries failed with %s", f.Count, f.Code)
		}
	}

	var delivered int64
	for ch, st := range after.Channels {
		b := before.Channels[ch]
		delivered += st.Delivered - b.Delivered
		if d := st.DuplicateContent - b.DuplicateContent; d != 0 {
			fail("%s: provider received %d duplicate messages", ch, d)
		}
		if d := st.Throttled - b.Throttled; d != 0 {
			fail("%s: provider throttled %d requests over its own %d/s limit", ch, d, o.maxRate)
		}
		if st.MaxPerSecond > o.maxRate {
			fail("%s: provider saw %d requests in one second, limit %d", ch, st.MaxPerSecond, o.maxRate)
		}
		logf("%s: %d delivered, %d resends suppressed, peak %d requests/s", ch,
			st.Delivered-b.Delivered, st.DuplicatesSuppressed-b.DuplicatesSuppressed, st.MaxPerSecond)
	}
	// A send accepted on the last attempt whose response was lost is
	// delivered but recorded as retries_exhausted (README, Known
	// limitations); any other difference is a lost or extra send.
	if delivered < t.Sent || delivered > t.Sent+exhausted {
		fail("provider delivered %d messages, campaign recorded %d sent (%d retries_exhausted)", delivered, t.Sent, exhausted)
	}

	seen := map[string]bool{}
	for _, m := range msgs {
		if m.DeliveredAt.Before(start) {
			continue
		}
		k := m.Channel + "\x00" + m.To
		if seen[k] {
			fail("a %s recipient received two messages", m.Channel)
		}
		seen[k] = true
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

type client struct{ http *http.Client }

func (c client) do(ctx context.Context, method, url string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, url, err)
	}
	return resp, nil
}

func send[T any](ctx context.Context, c client, method, url string, want int, body any) (T, error) {
	var v T
	resp, err := c.do(ctx, method, url, body)
	if err != nil {
		return v, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != want {
		return v, fmt.Errorf("%s %s: status %d: %s", method, url, resp.StatusCode, bytes.TrimSpace(data))
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return v, fmt.Errorf("%s %s: decode response: %w", method, url, err)
	}
	return v, nil
}

func getJSON[T any](ctx context.Context, c client, url string) (T, error) {
	return send[T](ctx, c, http.MethodGet, url, http.StatusOK, nil)
}

// waitReady polls a readiness endpoint until it answers 200.
func (c client) waitReady(ctx context.Context, url string) error {
	for {
		resp, err := c.do(ctx, http.MethodGet, url, nil)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s not ready (is the stack up?): %w", url, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

// streamDrafts reads the draft SSE stream to its done event and returns
// the number of deltas.
func (c client) streamDrafts(ctx context.Context, url string) (int, error) {
	resp, err := c.do(ctx, http.MethodPost, url, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return 0, fmt.Errorf("status %d: %s", resp.StatusCode, bytes.TrimSpace(data))
	}
	deltas, event := 0, ""
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			switch event {
			case "delta":
				deltas++
			case "done":
				return deltas, nil
			case "error":
				return deltas, fmt.Errorf("stream error: %s", strings.TrimPrefix(line, "data: "))
			}
		}
	}
	if err := sc.Err(); err != nil {
		return deltas, err
	}
	return deltas, errors.New("stream ended without a done event")
}

// waitCompleted polls campaign progress until the campaign completes.
func (c client) waitCompleted(ctx context.Context, url string) (httpapi.ProgressResponse, error) {
	last := time.Time{}
	for {
		p, err := getJSON[httpapi.ProgressResponse](ctx, c, url)
		if err != nil {
			return p, err
		}
		if p.CampaignStatus == campaign.StatusCompleted {
			return p, nil
		}
		if time.Since(last) >= 5*time.Second {
			last = time.Now()
			logf("  %s: %.0f%% done (%d sent, %d failed, %d retrying of %d)", p.CampaignStatus,
				p.PercentDone, p.Totals.Sent, p.Totals.Failed, p.Totals.Retrying, p.Totals.Total)
		}
		select {
		case <-ctx.Done():
			return p, fmt.Errorf("campaign did not complete: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func logf(format string, args ...any) { fmt.Printf(format+"\n", args...) }
