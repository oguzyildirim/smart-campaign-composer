package monitoring

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
)

// Hub errors.
var (
	// ErrTooManyViewers means the campaign already has the maximum number
	// of live viewers.
	ErrTooManyViewers = errors.New("too many viewers for this campaign")
	// ErrClosed means the hub is shutting down.
	ErrClosed = errors.New("progress hub closed")
)

// HubConfig paces the hub.
type HubConfig struct {
	// Interval is the time between two polls of one campaign.
	Interval time.Duration
	// Window is the throughput window of every snapshot.
	Window time.Duration
	// MaxViewers bounds the live subscriptions per campaign.
	MaxViewers int
}

// Hub serves campaign progress. Live viewers of a campaign share one
// poller: N viewers cost one query per interval, not N. A poller starts
// with the campaign's first subscription and stops when the last one is
// closed. Hub is safe for concurrent use.
type Hub struct {
	source Source
	cfg    HubConfig
	logger *slog.Logger

	mu      sync.Mutex
	closed  bool
	done    chan struct{} // closed by Close
	pollers map[campaign.ID]*poller
	wg      sync.WaitGroup
}

// poller is the shared poll loop of one campaign. Its subscribers are
// guarded by the hub's mutex.
type poller struct {
	subs   map[*Subscription]struct{}
	cancel context.CancelFunc
}

// NewHub returns a Hub reading from source.
func NewHub(source Source, cfg HubConfig, logger *slog.Logger) *Hub {
	return &Hub{source: source, cfg: cfg, logger: logger, done: make(chan struct{}), pollers: map[campaign.ID]*poller{}}
}

// Snapshot reads a campaign's progress directly, outside the shared
// pollers: for one-off requests and for a new viewer's first snapshot.
func (h *Hub) Snapshot(ctx context.Context, id campaign.ID) (Snapshot, error) {
	s, err := h.source.Progress(ctx, id, h.cfg.Window)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read campaign progress: %w", err)
	}
	return s, nil
}

// Subscription receives a campaign's snapshots from its shared poller,
// one per poll whether or not anything changed. Only the latest snapshot
// is kept: a slow reader skips intermediate ones, which is harmless
// because every snapshot is the full state.
type Subscription struct {
	c     chan Snapshot
	done  <-chan struct{}
	close func()
	once  sync.Once
}

// C delivers snapshots. It is never closed.
func (s *Subscription) C() <-chan Snapshot { return s.c }

// Done is closed when the hub shuts down; the viewer should then end its
// stream. The subscription must still be closed.
func (s *Subscription) Done() <-chan struct{} { return s.done }

// Close ends the subscription. It is idempotent.
func (s *Subscription) Close() { s.once.Do(s.close) }

// offer replaces any unread snapshot with snap. Only the campaign's poller
// sends, so the drain and the send cannot race with another sender.
func (s *Subscription) offer(snap Snapshot) {
	select {
	case <-s.c:
	default:
	}
	s.c <- snap
}

// Subscribe registers a live viewer of campaign id, starting the
// campaign's poller if it is the first. The caller must Close the
// subscription. It does not check that the campaign exists; callers read
// a Snapshot first for that.
func (h *Hub) Subscribe(id campaign.ID) (*Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosed
	}
	p := h.pollers[id]
	if p != nil && len(p.subs) >= h.cfg.MaxViewers {
		return nil, ErrTooManyViewers
	}
	if p == nil {
		ctx, cancel := context.WithCancel(context.Background())
		p = &poller{subs: map[*Subscription]struct{}{}, cancel: cancel}
		h.pollers[id] = p
		h.wg.Go(func() { h.poll(ctx, id, p) })
	}
	sub := &Subscription{c: make(chan Snapshot, 1), done: h.done}
	sub.close = func() { h.unsubscribe(id, p, sub) }
	p.subs[sub] = struct{}{}
	return sub, nil
}

// unsubscribe removes sub and stops its poller when it was the last one.
func (h *Hub) unsubscribe(id campaign.ID, p *poller, sub *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(p.subs, sub)
	if len(p.subs) == 0 {
		p.cancel()
		if h.pollers[id] == p {
			delete(h.pollers, id)
		}
	}
}

// poll reads the campaign's progress now and then every interval, and
// hands each snapshot to every subscriber, until ctx ends. A failed read
// is logged and retried at the next tick; subscribers keep their last
// snapshot meanwhile.
func (h *Hub) poll(ctx context.Context, id campaign.ID, p *poller) {
	ticker := time.NewTicker(h.cfg.Interval)
	defer ticker.Stop()
	for {
		snap, err := h.source.Progress(ctx, id, h.cfg.Window)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			h.logger.WarnContext(ctx, "campaign progress poll failed", "campaign_id", string(id), "error", err)
		default:
			h.mu.Lock()
			for sub := range p.subs {
				sub.offer(snap)
			}
			h.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Close stops every poller, closes every subscription's Done channel, and
// waits for the pollers to exit. Later Subscribe calls fail with
// ErrClosed; existing subscriptions receive nothing more.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	close(h.done)
	for id, p := range h.pollers {
		p.cancel()
		delete(h.pollers, id)
	}
	h.mu.Unlock()
	h.wg.Wait()
}
