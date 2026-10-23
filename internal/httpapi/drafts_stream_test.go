package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/llm/mockllm"
)

type sseEvent struct {
	name string
	data string
}

// readEvents parses an SSE stream until EOF. Comment lines are counted.
func readEvents(t *testing.T, r io.Reader) ([]sseEvent, int) {
	t.Helper()
	var (
		events   []sseEvent
		comments int
		cur      sseEvent
	)
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cur.name != "" {
				events = append(events, cur)
			}
			cur = sseEvent{}
		case strings.HasPrefix(line, ":"):
			comments++
		case strings.HasPrefix(line, "event: "):
			cur.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if cur.data != "" {
				t.Fatalf("multi-line data in event %q", cur.name)
			}
			cur.data = strings.TrimPrefix(line, "data: ")
		default:
			t.Fatalf("unexpected SSE line %q", line)
		}
	}
	return events, comments
}

func startDrafts(ctx context.Context, t *testing.T, srv *httptest.Server, id campaign.ID) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/campaigns/"+string(id)+"/drafts", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (f apiFixture) created(t *testing.T) campaign.Campaign {
	t.Helper()
	c, err := f.svc.Create(t.Context(), exampleBrief)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDraftStream_DeltasThenDone(t *testing.T) {
	f := newAPI(t, fixtureOptions{streamDelay: 5 * time.Millisecond, heartbeat: 2 * time.Millisecond})
	srv := httptest.NewServer(f.handler)
	t.Cleanup(srv.Close)
	c := f.created(t)

	resp := startDrafts(t.Context(), t, srv, c.ID)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" ||
		resp.Header.Get("Cache-Control") != "no-cache, no-transform" {
		t.Fatalf("response = %d %v", resp.StatusCode, resp.Header)
	}
	events, comments := readEvents(t, resp.Body)
	if len(events) < 2 || events[len(events)-1].name != eventDone {
		t.Fatalf("events = %+v", events)
	}
	if comments < 2 { // the initial comment plus at least one heartbeat
		t.Errorf("comments = %d, want heartbeats", comments)
	}
	streamed := map[campaign.DraftField]string{}
	for _, ev := range events[:len(events)-1] {
		if ev.name != eventDelta {
			t.Fatalf("event %q before done", ev.name)
		}
		var d DeltaEvent
		if err := json.Unmarshal([]byte(ev.data), &d); err != nil || d.Attempt != 1 {
			t.Fatalf("delta %q: %v", ev.data, err)
		}
		streamed[d.Field] += d.Text
	}
	var done DoneEvent
	if err := json.Unmarshal([]byte(events[len(events)-1].data), &done); err != nil {
		t.Fatal(err)
	}
	want := mockllm.GenerateDrafts(exampleBrief)
	if done.Status != campaign.StatusDraftsReady || done.Drafts.SMSBody != want.SMS.Body || !done.Validation.Valid ||
		done.Version != 2 || len(done.ContentHash) != 64 {
		t.Errorf("done = %+v", done)
	}
	if streamed[campaign.FieldSMSBody] != want.SMS.Body || streamed[campaign.FieldEmailSubject] != want.Email.Subject ||
		streamed[campaign.FieldEmailBody] != want.Email.Body {
		t.Errorf("streamed = %q", streamed)
	}
	stored, _ := f.svc.Get(t.Context(), c.ID)
	if stored.Version != done.Version || stored.ContentHash != done.ContentHash {
		t.Errorf("stored = %+v", stored)
	}
}

func TestDraftStream_ErrorEventOnInvalidOutput(t *testing.T) {
	f := newAPI(t, fixtureOptions{script: []mockllm.Step{{}, {Fault: mockllm.FaultOverlongSMS}, {Fault: mockllm.FaultOverlongSMS}}})
	srv := httptest.NewServer(f.handler)
	t.Cleanup(srv.Close)
	c := f.created(t)

	events, _ := readEvents(t, startDrafts(t.Context(), t, srv, c.ID).Body)
	last := events[len(events)-1]
	var ev StreamErrorEvent
	if err := json.Unmarshal([]byte(last.data), &ev); err != nil || last.name != eventError || ev.Code != "invalid_output" || ev.Message == "" {
		t.Fatalf("last event = %+v (%v)", last, err)
	}
	for _, e := range events {
		if e.name == eventDone {
			t.Error("done event after failed validation")
		}
	}
	stored, _ := f.svc.Get(t.Context(), c.ID)
	if stored.Status != campaign.StatusAudienceReady || stored.Version != c.Version || stored.GenerationToken != "" {
		t.Errorf("campaign after failed generation = %+v", stored)
	}
}

func TestDraftStream_ConflictsAndState(t *testing.T) {
	f := newAPI(t, fixtureOptions{})
	c := f.created(t)
	if _, err := f.svc.BeginGeneration(t.Context(), c.ID); err != nil {
		t.Fatal(err)
	}
	requireError(t, f.do(http.MethodPost, "/campaigns/"+string(c.ID)+"/drafts", ""), http.StatusConflict, "generation_in_progress")
	requireError(t, f.do(http.MethodPost, "/campaigns/00000000-0000-4000-8000-999999999999/drafts", ""), http.StatusNotFound, "not_found")

	approved := f.ready(t)
	if _, err := f.svc.Approve(t.Context(), approved.ID, campaign.Approval{
		Version: approved.Version, ContentHash: approved.ContentHash, ApprovedBy: "x",
	}); err != nil {
		t.Fatal(err)
	}
	requireError(t, f.do(http.MethodPost, "/campaigns/"+string(approved.ID)+"/drafts", ""), http.StatusConflict, "invalid_state")
}

func TestDraftStream_ClientDisconnectCancelsGeneration(t *testing.T) {
	f := newAPI(t, fixtureOptions{streamDelay: time.Hour})
	srv := httptest.NewServer(f.handler)
	t.Cleanup(srv.Close)
	c := f.created(t)

	ctx, cancel := context.WithCancel(t.Context())
	resp := startDrafts(ctx, t, srv, c.ID)
	// Read up to the first delta, then leave like a closed tab.
	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before a delta: %v", err)
		}
		if strings.HasPrefix(line, "event: "+eventDelta) {
			break
		}
	}
	cancel()

	// The mock blocks for an hour between chunks, so only cancellation can
	// end the generation and release the lease.
	deadline := time.Now().Add(5 * time.Second)
	for {
		stored, err := f.svc.Get(t.Context(), c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.GenerationToken == "" {
			if stored.Status != campaign.StatusAudienceReady || stored.Version != c.Version {
				t.Errorf("campaign after disconnect = %+v", stored)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("generation still holds the lease after the client disconnected")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := f.svc.BeginGeneration(t.Context(), c.ID); err != nil {
		t.Errorf("a new generation cannot start: %v", err)
	}
}
