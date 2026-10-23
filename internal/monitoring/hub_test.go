package monitoring

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
)

const (
	campaignA campaign.ID = "0196f0a2-7c1e-7b3a-9d2e-3f4a5b6c7d8e"
	campaignB campaign.ID = "0196f0a2-7c1e-7b3a-9d2e-3f4a5b6c7d8f"
)

var discard = slog.New(slog.DiscardHandler)

// fakeSource returns a scripted number of sent messages per campaign and
// counts queries.
type fakeSource struct {
	mu      sync.Mutex
	sent    map[campaign.ID]int64
	queries map[campaign.ID]int
	err     error
}

func newFakeSource() *fakeSource {
	return &fakeSource{sent: map[campaign.ID]int64{}, queries: map[campaign.ID]int{}}
}

func (f *fakeSource) Progress(_ context.Context, id campaign.ID, window time.Duration) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries[id]++
	if f.err != nil {
		return Snapshot{}, f.err
	}
	return Snapshot{
		CampaignID:     id,
		CampaignStatus: campaign.StatusSending,
		Channels:       []ChannelProgress{{Channel: content.ChannelSMS, Counts: StatusCounts{Sent: f.sent[id]}}},
		Window:         window,
		GeneratedAt:    time.Now(),
	}, nil
}

func (f *fakeSource) setSent(id campaign.ID, n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent[id] = n
}

func (f *fakeSource) queryCount(id campaign.ID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries[id]
}

func newHub(t *testing.T, src Source, maxViewers int) *Hub {
	t.Helper()
	h := NewHub(src, HubConfig{Interval: 5 * time.Millisecond, Window: 10 * time.Second, MaxViewers: maxViewers}, discard)
	t.Cleanup(h.Close)
	return h
}

func receive(t *testing.T, s *Subscription) Snapshot {
	t.Helper()
	select {
	case snap := <-s.C():
		return snap
	case <-time.After(2 * time.Second):
		t.Fatal("no snapshot within 2s")
		return Snapshot{}
	}
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHub_SnapshotReadsSource(t *testing.T) {
	src := newFakeSource()
	src.setSent(campaignA, 7)
	h := newHub(t, src, 10)

	snap, err := h.Snapshot(t.Context(), campaignA)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Totals().Sent != 7 || snap.Window != 10*time.Second {
		t.Errorf("snapshot = %+v, want 7 sent over a 10s window", snap)
	}
}

func TestHub_SnapshotPassesNotFound(t *testing.T) {
	src := newFakeSource()
	src.err = campaign.ErrNotFound
	h := newHub(t, src, 10)

	if _, err := h.Snapshot(t.Context(), campaignA); !errors.Is(err, campaign.ErrNotFound) {
		t.Errorf("Snapshot() error = %v, want campaign.ErrNotFound", err)
	}
}

func TestHub_SubscriberReceivesLatestState(t *testing.T) {
	src := newFakeSource()
	h := newHub(t, src, 10)

	sub, err := h.Subscribe(campaignA)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	src.setSent(campaignA, 42)
	eventually(t, "the subscriber sees 42 sent", func() bool { return receive(t, sub).Totals().Sent == 42 })
}

func TestHub_ViewersOfOneCampaignShareOnePoller(t *testing.T) {
	src := newFakeSource()
	h := newHub(t, src, 10)

	first, err := h.Subscribe(campaignA)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.Subscribe(campaignA)
	if err != nil {
		t.Fatal(err)
	}
	receive(t, first)
	receive(t, second)

	// Over a fixed period, one shared poller queries about once per
	// interval; one poller per viewer would query twice as often.
	before := src.queryCount(campaignA)
	time.Sleep(100 * time.Millisecond)
	queries := src.queryCount(campaignA) - before
	if queries > 100/5+5 {
		t.Errorf("%d queries in 100ms at a 5ms interval for two viewers, want one poller (~20)", queries)
	}

	first.Close()
	second.Close()
	stopped := src.queryCount(campaignA)
	time.Sleep(50 * time.Millisecond)
	// At most one poll that was already running when the last viewer left.
	if after := src.queryCount(campaignA); after > stopped+1 {
		t.Errorf("source queried %d more times after every viewer left", after-stopped)
	}
}

func TestHub_CampaignsArePolledSeparately(t *testing.T) {
	src := newFakeSource()
	src.setSent(campaignA, 1)
	src.setSent(campaignB, 2)
	h := newHub(t, src, 10)

	a, _ := h.Subscribe(campaignA)
	defer a.Close()
	b, _ := h.Subscribe(campaignB)
	defer b.Close()

	if got := receive(t, a); got.CampaignID != campaignA || got.Totals().Sent != 1 {
		t.Errorf("campaign A viewer got %+v", got)
	}
	if got := receive(t, b); got.CampaignID != campaignB || got.Totals().Sent != 2 {
		t.Errorf("campaign B viewer got %+v", got)
	}
}

func TestHub_RejectsViewersOverTheLimit(t *testing.T) {
	h := newHub(t, newFakeSource(), 2)

	for range 2 {
		s, err := h.Subscribe(campaignA)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
	}
	if _, err := h.Subscribe(campaignA); !errors.Is(err, ErrTooManyViewers) {
		t.Fatalf("third Subscribe() error = %v, want ErrTooManyViewers", err)
	}
	// The limit is per campaign.
	other, err := h.Subscribe(campaignB)
	if err != nil {
		t.Fatalf("Subscribe() to another campaign: %v", err)
	}
	other.Close()
}

func TestHub_LeavingFreesAViewerSlot(t *testing.T) {
	h := newHub(t, newFakeSource(), 1)

	s, err := h.Subscribe(campaignA)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s.Close() // idempotent
	again, err := h.Subscribe(campaignA)
	if err != nil {
		t.Fatalf("Subscribe() after the only viewer left: %v", err)
	}
	again.Close()
}

func TestHub_PollErrorsKeepThePollerRunning(t *testing.T) {
	src := newFakeSource()
	src.err = errors.New("database down")
	h := newHub(t, src, 10)

	sub, _ := h.Subscribe(campaignA)
	defer sub.Close()
	eventually(t, "the poller retries", func() bool { return src.queryCount(campaignA) >= 3 })

	src.mu.Lock()
	src.err = nil
	src.mu.Unlock()
	src.setSent(campaignA, 5)
	if got := receive(t, sub); got.Totals().Sent != 5 {
		t.Errorf("after recovery got %+v, want 5 sent", got)
	}
}

func TestHub_NoGoroutinesLeakAfterViewersLeave(t *testing.T) {
	src := newFakeSource()
	h := NewHub(src, HubConfig{Interval: time.Millisecond, Window: time.Second, MaxViewers: 50}, discard)
	before := runtime.NumGoroutine()

	var subs []*Subscription
	for _, id := range []campaign.ID{campaignA, campaignB} {
		for range 10 {
			s, err := h.Subscribe(id)
			if err != nil {
				t.Fatal(err)
			}
			subs = append(subs, s)
		}
	}
	for _, s := range subs {
		receive(t, s)
		s.Close()
	}
	eventually(t, "poller goroutines exit", func() bool { return runtime.NumGoroutine() <= before })
	h.Close()
}

func TestHub_CloseStopsPollersAndRejectsSubscribers(t *testing.T) {
	var polls atomic.Int64
	src := sourceFunc(func(context.Context, campaign.ID, time.Duration) (Snapshot, error) {
		polls.Add(1)
		return Snapshot{}, nil
	})
	h := NewHub(src, HubConfig{Interval: time.Millisecond, Window: time.Second, MaxViewers: 5}, discard)
	sub, _ := h.Subscribe(campaignA)
	eventually(t, "polling starts", func() bool { return polls.Load() > 0 })

	select {
	case <-sub.Done():
		t.Fatal("Done closed before the hub closed")
	default:
	}
	h.Close()
	select {
	case <-sub.Done():
	default:
		t.Error("Done not closed after Close: streams would outlive shutdown")
	}
	stopped := polls.Load()
	time.Sleep(20 * time.Millisecond)
	if polls.Load() != stopped {
		t.Error("poller kept running after Close")
	}
	if _, err := h.Subscribe(campaignA); !errors.Is(err, ErrClosed) {
		t.Errorf("Subscribe() after Close error = %v, want ErrClosed", err)
	}
	sub.Close() // safe after the hub closed
}

type sourceFunc func(context.Context, campaign.ID, time.Duration) (Snapshot, error)

func (f sourceFunc) Progress(ctx context.Context, id campaign.ID, w time.Duration) (Snapshot, error) {
	return f(ctx, id, w)
}
