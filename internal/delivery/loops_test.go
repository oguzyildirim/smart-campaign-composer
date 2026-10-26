package delivery

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
)

var discard = slog.New(slog.DiscardHandler)

func TestDecodeJob(t *testing.T) {
	id, err := DecodeJob(EncodeJob("0199f0a0-0000-7000-8000-00000000000a"))
	if err != nil || id != "0199f0a0-0000-7000-8000-00000000000a" {
		t.Fatalf("round trip = %q, %v", id, err)
	}
	for _, bad := range []string{
		``, `{}`, `{"delivery_id":"x"}`, `{"delivery_id":"0199f0a0-0000-7000-8000-00000000000a","extra":1}`,
		`{"delivery_id":"0199f0a0-0000-7000-8000-00000000000a"}{}`, `{"delivery_id":"1'; DROP TABLE deliveries;--"}`,
	} {
		if _, err := DecodeJob([]byte(bad)); err == nil {
			t.Errorf("DecodeJob(%q) accepted", bad)
		}
	}
}

func TestHandle_DropsMalformedJobs(t *testing.T) {
	p := NewProcessor(nil, nil, nil, ProcessorConfig{}, discard) // nil ports: must not be reached
	if err := p.Handle(t.Context(), []byte(`not json`)); err != nil {
		t.Fatalf("Handle() error = %v, want nil (drop)", err)
	}
}

// fakeOutbox holds pending messages; failNext makes the next publish fail.
type fakeOutbox struct {
	mu        sync.Mutex
	pending   []OutboxMessage
	published []OutboxMessage
	failNext  int
}

func (o *fakeOutbox) PublishPending(ctx context.Context, limit int, publish func(context.Context, []OutboxMessage) error) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	batch := o.pending[:min(limit, len(o.pending))]
	if len(batch) == 0 {
		return 0, nil
	}
	if o.failNext > 0 {
		o.failNext--
		return 0, errors.New("broker unavailable")
	}
	if err := publish(ctx, batch); err != nil {
		return 0, err
	}
	o.published = append(o.published, batch...)
	o.pending = o.pending[len(batch):]
	return len(batch), nil
}

func (o *fakeOutbox) done() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.pending) == 0
}

type nopPublisher struct{}

func (nopPublisher) Publish(context.Context, []OutboxMessage) error { return nil }

func TestRunRelay_PublishesEverythingAndSurvivesFailures(t *testing.T) {
	o := &fakeOutbox{failNext: 2}
	for i := range 25 {
		o.pending = append(o.pending, OutboxMessage{ID: int64(i + 1)})
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- RunRelay(ctx, o, nopPublisher{}, RelayConfig{BatchSize: 10, PollInterval: time.Millisecond}, discard)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !o.done() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunRelay() = %v", err)
	}
	if len(o.published) != 25 {
		t.Errorf("published %d messages, want 25", len(o.published))
	}
}

type fakeMaintenance struct {
	mu                         sync.Mutex
	due                        int
	recovered, requeued, ticks int
	completed, purged          int
}

func (m *fakeMaintenance) RecoverExpiredLeases(context.Context, int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ticks++
	m.recovered++
	return 1, nil
}

func (m *fakeMaintenance) RequeueDue(_ context.Context, limit int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := min(limit, m.due)
	m.due -= n
	m.requeued += n
	return n, nil
}

func (m *fakeMaintenance) CompleteFinishedCampaigns(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completed++
	return 0, nil
}

func (m *fakeMaintenance) PendingOutbox(context.Context) (int, error) { return 0, nil }

func (m *fakeMaintenance) PurgePublishedOutbox(context.Context, time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purged++
	return 0, errors.New("ignored")
}

func TestRunMaintenance_DrainsDueRetriesEachTick(t *testing.T) {
	m := &fakeMaintenance{due: 35}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- RunMaintenance(ctx, m, MaintenanceConfig{Interval: time.Hour, BatchSize: 10}, discard)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.Lock()
		finished := m.completed > 0
		m.mu.Unlock()
		if finished || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.requeued != 35 || m.recovered != 1 || m.completed != 1 || m.purged != 1 {
		t.Errorf("one tick did requeued=%d recovered=%d completed=%d purged=%d, want 35/1/1/1",
			m.requeued, m.recovered, m.completed, m.purged)
	}
}

// fakeReleaser has due campaigns to release; failFirst fails its first
// call.
type fakeReleaser struct {
	mu        sync.Mutex
	due       int
	released  int
	failFirst bool
	calls     int
}

func (r *fakeReleaser) ReleaseDue(_ context.Context, limit int) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.failFirst && r.calls == 1 {
		return 0, errors.New("database unavailable")
	}
	n := min(limit, r.due)
	r.due -= n
	r.released += n
	return n, nil
}

func (r *fakeReleaser) state() (released, calls int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.released, r.calls
}

func TestRunMaintenance_ReleasesDueCampaigns(t *testing.T) {
	m := &fakeMaintenance{}
	r := &fakeReleaser{due: 25, failFirst: true}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- RunMaintenance(ctx, m, MaintenanceConfig{Interval: time.Millisecond, BatchSize: 10, Releaser: r}, discard)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		released, _ := r.state()
		if released == 25 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The failed first call is retried on the next tick; full batches are
	// drained within a tick.
	if released, calls := r.state(); released != 25 || calls < 4 {
		t.Errorf("released = %d after %d calls, want 25 after at least 4", released, calls)
	}
}
