package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/monitoring"
)

const progressID campaign.ID = "0196f0a2-7c1e-7b3a-9d2e-3f4a5b6c7d8e"

var generatedAt = time.Date(2026, 10, 24, 9, 30, 0, 0, time.UTC)

// progressSource is a scriptable monitoring.Source.
type progressSource struct {
	mu      sync.Mutex
	snap    monitoring.Snapshot
	err     error
	queries int
}

func (s *progressSource) Progress(_ context.Context, id campaign.ID, window time.Duration) (monitoring.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries++
	if s.err != nil {
		return monitoring.Snapshot{}, s.err
	}
	snap := s.snap
	// Copies: callers keep snapshots while the test mutates the script.
	snap.Channels, snap.Failures = slices.Clone(snap.Channels), slices.Clone(snap.Failures)
	snap.CampaignID, snap.Window = id, window
	return snap, nil
}

func (s *progressSource) set(f func(*monitoring.Snapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap.Channels, s.snap.Failures = slices.Clone(s.snap.Channels), slices.Clone(s.snap.Failures)
	f(&s.snap)
	// Every poll is generated anew; only state changes may produce events.
	s.snap.GeneratedAt = s.snap.GeneratedAt.Add(time.Second)
}

func (s *progressSource) queryCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queries
}

func sendingSnapshot() monitoring.Snapshot {
	return monitoring.Snapshot{
		CampaignStatus: campaign.StatusSending,
		Channels: []monitoring.ChannelProgress{
			{Channel: content.ChannelSMS, Counts: monitoring.StatusCounts{Queued: 2, Processing: 1, Sent: 4, Failed: 1, Retrying: 2}, RecentSent: 3},
			{Channel: content.ChannelEmail, Counts: monitoring.StatusCounts{Sent: 5, Failed: 1}, RecentSent: 2},
		},
		Failures:    []monitoring.FailureCount{{Code: "provider_rejected", Count: 2}},
		GeneratedAt: generatedAt,
	}
}

type progressEnv struct {
	src *progressSource
	srv *httptest.Server
}

func newProgressEnv(t *testing.T, maxViewers int) progressEnv {
	t.Helper()
	src := &progressSource{snap: sendingSnapshot()}
	logger := slog.New(slog.DiscardHandler)
	hub := monitoring.NewHub(src, monitoring.HubConfig{Interval: 5 * time.Millisecond, Window: 10 * time.Second, MaxViewers: maxViewers}, logger)
	r := chi.NewRouter()
	MountProgress(r, ProgressRoutes{Service: hub, Logger: logger, RequestTimeout: time.Second, Heartbeat: 10 * time.Millisecond})
	srv := httptest.NewServer(r)
	// LIFO: close the server (and its streams) before the hub.
	t.Cleanup(hub.Close)
	t.Cleanup(srv.Close)
	return progressEnv{src: src, srv: srv}
}

func TestProgress_ReturnsSnapshotJSON(t *testing.T) {
	e := newProgressEnv(t, 5)

	resp, err := http.Get(e.srv.URL + "/campaigns/" + string(progressID) + "/progress")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"campaign_id":     string(progressID),
		"campaign_status": "sending",
		"channels": []any{
			map[string]any{"channel": "sms", "queued": 2.0, "processing": 1.0, "sent": 4.0, "failed": 1.0, "retrying": 2.0, "total": 10.0, "sent_recently": 3.0, "percent_done": 50.0},
			map[string]any{"channel": "email", "queued": 0.0, "processing": 0.0, "sent": 5.0, "failed": 1.0, "retrying": 0.0, "total": 6.0, "sent_recently": 2.0, "percent_done": 100.0},
		},
		"totals":       map[string]any{"queued": 2.0, "processing": 1.0, "sent": 9.0, "failed": 2.0, "retrying": 2.0, "total": 16.0},
		"percent_done": 68.75,
		"failures":     []any{map[string]any{"code": "provider_rejected", "count": 2.0}},
		"throughput":   map[string]any{"window_seconds": 10.0, "sent": 5.0, "per_second": 0.5},
		"generated_at": "2026-10-24T09:30:00Z",
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("body =\n%s\nwant\n%s", gotJSON, wantJSON)
	}
}

func TestProgress_Errors(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"unknown campaign", "/campaigns/" + string(progressID) + "/progress", campaign.ErrNotFound, http.StatusNotFound, "not_found"},
		{"invalid id", "/campaigns/not-a-uuid/progress", nil, http.StatusNotFound, "not_found"},
		{"store failure", "/campaigns/" + string(progressID) + "/progress", errors.New("pq: secret internal detail"), http.StatusInternalServerError, "internal"},
		{"events of unknown campaign", "/campaigns/" + string(progressID) + "/events", campaign.ErrNotFound, http.StatusNotFound, "not_found"},
		{"events with invalid id", "/campaigns/x/events", nil, http.StatusNotFound, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newProgressEnv(t, 5)
			e.src.err = tt.err
			resp, err := http.Get(e.srv.URL + tt.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			var er ErrorResponse
			if err := json.Unmarshal(body, &er); err != nil {
				t.Fatalf("body %q is not a JSON error: %v", body, err)
			}
			if resp.StatusCode != tt.wantStatus || er.Code != tt.wantCode {
				t.Errorf("got %d %q, want %d %q", resp.StatusCode, er.Code, tt.wantStatus, tt.wantCode)
			}
			if strings.Contains(string(body), "secret") {
				t.Errorf("error body leaks internal detail: %s", body)
			}
		})
	}
}

// liveEvent is one parsed SSE event, with its id.
type liveEvent struct {
	id, name, data string
}

// eventStream reads a live SSE response event by event.
type eventStream struct {
	t        *testing.T
	resp     *http.Response
	events   chan liveEvent
	comments chan struct{}
	cancel   context.CancelFunc
}

func openEvents(t *testing.T, e progressEnv, lastEventID string) *eventStream {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/campaigns/"+string(progressID)+"/events", nil)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	s := &eventStream{t: t, resp: resp, events: make(chan liveEvent, 64), comments: make(chan struct{}, 1024), cancel: cancel}
	t.Cleanup(func() { s.close() })
	if resp.StatusCode != http.StatusOK {
		return s
	}
	go func() {
		defer close(s.events)
		sc := bufio.NewScanner(resp.Body)
		var cur liveEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur.name != "" {
					s.events <- cur
				}
				cur = liveEvent{}
			case strings.HasPrefix(line, ":"):
				select {
				case s.comments <- struct{}{}:
				default:
				}
			case strings.HasPrefix(line, "id: "):
				cur.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				cur.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			case strings.HasPrefix(line, "retry: "):
			default:
				t.Errorf("unexpected SSE line %q", line)
			}
		}
	}()
	return s
}

func (s *eventStream) close() {
	s.cancel()
	_ = s.resp.Body.Close()
}

// next returns the next event, failing after a timeout or at EOF.
func (s *eventStream) next() liveEvent {
	s.t.Helper()
	select {
	case ev, ok := <-s.events:
		if !ok {
			s.t.Fatal("stream ended, want another event")
		}
		return ev
	case <-time.After(2 * time.Second):
		s.t.Fatal("no event within 2s")
		return liveEvent{}
	}
}

// quiet asserts that no event arrives for d.
func (s *eventStream) quiet(d time.Duration) {
	s.t.Helper()
	select {
	case ev, ok := <-s.events:
		if ok {
			s.t.Fatalf("unexpected event %+v", ev)
		}
		s.t.Fatal("stream ended unexpectedly")
	case <-time.After(d):
	}
}

// ended asserts that the server closes the stream.
func (s *eventStream) ended() {
	s.t.Helper()
	select {
	case ev, ok := <-s.events:
		if ok {
			s.t.Fatalf("unexpected event %+v, want end of stream", ev)
		}
	case <-time.After(2 * time.Second):
		s.t.Fatal("stream still open after 2s")
	}
}

func sentIn(t *testing.T, ev liveEvent) float64 {
	t.Helper()
	var body struct {
		Totals map[string]float64 `json:"totals"`
	}
	if err := json.Unmarshal([]byte(ev.data), &body); err != nil {
		t.Fatalf("event data %q: %v", ev.data, err)
	}
	return body.Totals["sent"]
}

func TestEvents_SnapshotOnConnectThenOnlyChanges(t *testing.T) {
	e := newProgressEnv(t, 5)
	s := openEvents(t, e, "")
	if ct := s.resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	first := s.next()
	if first.name != "snapshot" || first.id != "1" || sentIn(t, first) != 9 {
		t.Fatalf("first event = %+v, want snapshot id 1 with 9 sent", first)
	}
	// The poller keeps reading the same state: nothing is pushed.
	s.quiet(60 * time.Millisecond)

	e.src.set(func(s *monitoring.Snapshot) { s.Channels[0].Counts.Queued--; s.Channels[0].Counts.Sent++ })
	second := s.next()
	if second.name != "snapshot" || second.id != "2" || sentIn(t, second) != 10 {
		t.Fatalf("second event = %+v, want snapshot id 2 with 10 sent", second)
	}
	s.quiet(30 * time.Millisecond)

	select {
	case <-s.comments:
	case <-time.After(time.Second):
		t.Error("no heartbeat comment")
	}
}

func TestEvents_CompleteEventThenClose(t *testing.T) {
	e := newProgressEnv(t, 5)
	s := openEvents(t, e, "")
	s.next()

	e.src.set(func(s *monitoring.Snapshot) {
		s.CampaignStatus = campaign.StatusCompleted
		s.Channels[0].Counts = monitoring.StatusCounts{Sent: 8, Failed: 2}
	})
	ev := s.next()
	if ev.name != "complete" || ev.id != "2" || !strings.Contains(ev.data, `"campaign_status":"completed"`) {
		t.Fatalf("event = %+v, want complete with the final snapshot", ev)
	}
	s.ended()
}

func TestEvents_CompletedCampaignSendsCompleteOnConnect(t *testing.T) {
	e := newProgressEnv(t, 5)
	e.src.set(func(s *monitoring.Snapshot) { s.CampaignStatus = campaign.StatusCompleted })
	s := openEvents(t, e, "")
	if ev := s.next(); ev.name != "complete" || ev.id != "1" {
		t.Fatalf("event = %+v, want complete id 1", ev)
	}
	s.ended()
}

func TestEvents_LastEventIDContinuesTheSequenceWithAFreshSnapshot(t *testing.T) {
	e := newProgressEnv(t, 5)
	e.src.set(func(s *monitoring.Snapshot) { s.Channels[1].Counts.Sent = 50 })

	s := openEvents(t, e, "41")
	ev := s.next()
	if ev.name != "snapshot" || ev.id != "42" || sentIn(t, ev) != 54 {
		t.Fatalf("event = %+v, want a full snapshot with id 42 and 54 sent", ev)
	}

	for _, bad := range []string{"abc", "-3", "99999999999999999999999"} {
		s := openEvents(t, e, bad)
		if ev := s.next(); ev.id != "1" {
			t.Errorf("Last-Event-ID %q: first id = %s, want 1", bad, ev.id)
		}
	}
}

func TestEvents_DisconnectReleasesTheSubscription(t *testing.T) {
	e := newProgressEnv(t, 1)
	s := openEvents(t, e, "")
	s.next()
	s.close()

	// With the only viewer gone the poller stops...
	deadline := time.Now().Add(2 * time.Second)
	for {
		before := e.src.queryCount()
		time.Sleep(30 * time.Millisecond)
		if e.src.queryCount() == before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("source still polled after the client disconnected")
		}
	}
	// ...and the viewer slot is free again.
	again := openEvents(t, e, "")
	if again.resp.StatusCode != http.StatusOK {
		t.Fatalf("reconnect status = %d, want 200", again.resp.StatusCode)
	}
	again.next()
}

func TestEvents_ViewerLimit(t *testing.T) {
	e := newProgressEnv(t, 1)
	s := openEvents(t, e, "")
	s.next()

	over := openEvents(t, e, "")
	body, _ := io.ReadAll(over.resp.Body)
	var er ErrorResponse
	_ = json.Unmarshal(body, &er)
	if over.resp.StatusCode != http.StatusTooManyRequests || er.Code != "too_many_viewers" {
		t.Errorf("got %d %s, want 429 too_many_viewers", over.resp.StatusCode, body)
	}
}

func TestEvents_SharedPollerForTwoViewers(t *testing.T) {
	e := newProgressEnv(t, 5)
	a := openEvents(t, e, "")
	b := openEvents(t, e, "")
	a.next()
	b.next()

	before := e.src.queryCount()
	time.Sleep(100 * time.Millisecond)
	// One poller at a 5ms interval: about 20 queries; two would be ~40.
	if n := e.src.queryCount() - before; n > 25 {
		t.Errorf("%d queries in 100ms for two viewers, want one shared poller", n)
	}

	e.src.set(func(s *monitoring.Snapshot) { s.Channels[0].Counts.Sent++ })
	if sentIn(t, a.next()) != 10 || sentIn(t, b.next()) != 10 {
		t.Error("both viewers should receive the change")
	}
}
