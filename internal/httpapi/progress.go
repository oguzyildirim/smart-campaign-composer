package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/monitoring"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/httpserver"
)

// SSE event names of the progress stream.
const (
	eventSnapshot = "snapshot"
	eventComplete = "complete"
)

// codeTooManyViewers rejects a progress stream over the per-campaign limit.
const codeTooManyViewers = "too_many_viewers"

// progressRetry is the reconnection delay suggested to EventSource clients.
const progressRetry = 2 * time.Second

// ProgressService serves campaign progress. *monitoring.Hub implements it.
type ProgressService interface {
	Snapshot(ctx context.Context, id campaign.ID) (monitoring.Snapshot, error)
	Subscribe(id campaign.ID) (*monitoring.Subscription, error)
}

// ProgressRoutes configures the progress endpoints.
type ProgressRoutes struct {
	Service ProgressService
	Logger  *slog.Logger
	// RequestTimeout bounds the JSON endpoint. The event stream has no
	// timeout; it ends when the client leaves or the campaign completes.
	RequestTimeout time.Duration
	// Heartbeat is the interval of SSE keep-alive comments.
	Heartbeat time.Duration
}

// MountProgress registers the progress routes on r.
func MountProgress(r chi.Router, cfg ProgressRoutes) {
	h := progressHandler{svc: cfg.Service, logger: cfg.Logger, heartbeat: cfg.Heartbeat}
	r.With(middleware.Timeout(cfg.RequestTimeout)).Get("/campaigns/{id}/progress", h.progress)
	r.Get("/campaigns/{id}/events", h.events)
}

type progressHandler struct {
	svc       ProgressService
	logger    *slog.Logger
	heartbeat time.Duration
}

// ProgressResponse is a campaign's delivery progress: the body of
// GET /progress and the data of every progress stream event. It is the
// full state, so clients replace what they show with each one.
type ProgressResponse struct {
	CampaignID     string            `json:"campaign_id"`
	CampaignStatus campaign.Status   `json:"campaign_status"`
	Channels       []ChannelCounts   `json:"channels"`
	Totals         StatusCounts      `json:"totals"`
	PercentDone    float64           `json:"percent_done"`
	Failures       []FailureResponse `json:"failures"`
	Throughput     Throughput        `json:"throughput"`
	GeneratedAt    time.Time         `json:"generated_at"`
}

// StatusCounts counts deliveries by status.
type StatusCounts struct {
	Queued     int64 `json:"queued"`
	Processing int64 `json:"processing"`
	Sent       int64 `json:"sent"`
	Failed     int64 `json:"failed"`
	Retrying   int64 `json:"retrying"`
	Total      int64 `json:"total"`
}

// ChannelCounts is one channel's deliveries. SentRecently counts sends
// within the throughput window; PercentDone is the channel's share of
// sent or failed deliveries.
type ChannelCounts struct {
	Channel content.Channel `json:"channel"`
	StatusCounts
	SentRecently int64   `json:"sent_recently"`
	PercentDone  float64 `json:"percent_done"`
}

// FailureResponse is the number of failed deliveries with one error code.
type FailureResponse struct {
	Code  string `json:"code"`
	Count int64  `json:"count"`
}

// Throughput is the recent send rate across channels.
type Throughput struct {
	WindowSeconds float64 `json:"window_seconds"`
	Sent          int64   `json:"sent"`
	PerSecond     float64 `json:"per_second"`
}

func statusCounts(c monitoring.StatusCounts) StatusCounts {
	return StatusCounts{Queued: c.Queued, Processing: c.Processing, Sent: c.Sent, Failed: c.Failed, Retrying: c.Retrying, Total: c.Total()}
}

func progressResponse(s monitoring.Snapshot) ProgressResponse {
	out := ProgressResponse{
		CampaignID:     string(s.CampaignID),
		CampaignStatus: s.CampaignStatus,
		Channels:       make([]ChannelCounts, len(s.Channels)),
		Totals:         statusCounts(s.Totals()),
		PercentDone:    s.PercentDone(),
		Failures:       make([]FailureResponse, len(s.Failures)),
		Throughput:     Throughput{WindowSeconds: s.Window.Seconds(), Sent: s.RecentSent(), PerSecond: s.Throughput()},
		GeneratedAt:    s.GeneratedAt,
	}
	for i, ch := range s.Channels {
		out.Channels[i] = ChannelCounts{
			Channel: ch.Channel, StatusCounts: statusCounts(ch.Counts), SentRecently: ch.RecentSent, PercentDone: ch.Counts.PercentDone(),
		}
	}
	for i, f := range s.Failures {
		out.Failures[i] = FailureResponse{Code: f.Code, Count: f.Count}
	}
	return out
}

func (h progressHandler) progress(w http.ResponseWriter, r *http.Request) {
	id, ok := parseCampaignID(w, r)
	if !ok {
		return
	}
	snap, err := h.svc.Snapshot(r.Context(), id)
	if err != nil {
		h.writeError(w, r, "read campaign progress", err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, progressResponse(snap))
}

// events streams a campaign's progress as Server-Sent Events for
// EventSource. It sends the current snapshot on connect and afterwards
// each snapshot that differs from the last one sent; when the campaign is
// completed it sends a final complete event and closes. Event ids
// continue from Last-Event-ID, so they keep increasing across reconnects;
// a reconnect always starts with a fresh full snapshot.
func (h progressHandler) events(w http.ResponseWriter, r *http.Request) {
	id, ok := parseCampaignID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	// Subscribe before reading the first snapshot, so no change between
	// the two is missed.
	sub, err := h.svc.Subscribe(id)
	if err != nil {
		h.writeError(w, r, "subscribe to campaign progress", err)
		return
	}
	defer sub.Close()
	last, err := h.svc.Snapshot(ctx, id)
	if err != nil {
		h.writeError(w, r, "read campaign progress", err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering (nginx)
	w.WriteHeader(http.StatusOK)
	stream := newSSEWriter(w)
	if stream.retry(progressRetry) != nil {
		return
	}

	seq := lastEventID(r)
	send := func(s monitoring.Snapshot) (done bool) {
		seq++
		name := eventSnapshot
		if s.Completed() {
			name = eventComplete
		}
		if err := stream.eventWithID(seq, name, progressResponse(s)); err != nil {
			return true // the client is gone
		}
		return s.Completed()
	}
	if send(last) {
		return
	}

	var heartbeat <-chan time.Time
	if h.heartbeat > 0 {
		ticker := time.NewTicker(h.heartbeat)
		defer ticker.Stop()
		heartbeat = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.Done():
			return // server shutdown; the client reconnects to another instance or later
		case <-heartbeat:
			if stream.comment("heartbeat") != nil {
				return
			}
		case s := <-sub.C():
			if s.SameState(last) {
				continue
			}
			last = s
			if send(s) {
				return
			}
		}
	}
}

// maxEventID bounds the Last-Event-ID a client may send, keeping ids well
// inside the integers JavaScript represents exactly.
const maxEventID = 1 << 50

// lastEventID returns the client's Last-Event-ID, or 0 when it is absent
// or not a plausible id.
func lastEventID(r *http.Request) uint64 {
	n, err := strconv.ParseUint(r.Header.Get("Last-Event-ID"), 10, 64)
	if err != nil || n > maxEventID {
		return 0
	}
	return n
}

// parseCampaignID parses the {id} path parameter; anything but a UUID is
// 404.
func parseCampaignID(w http.ResponseWriter, r *http.Request) (campaign.ID, bool) {
	id, err := campaign.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		writeCampaignError(w, http.StatusNotFound, string(campaign.CodeNotFound), "campaign not found", nil, nil)
		return "", false
	}
	return id, true
}

func (h progressHandler) writeError(w http.ResponseWriter, r *http.Request, op string, err error) {
	ctx := r.Context()
	switch {
	case errors.Is(err, campaign.ErrNotFound):
		writeCampaignError(w, http.StatusNotFound, string(campaign.CodeNotFound), "campaign not found", nil, nil)
	case errors.Is(err, monitoring.ErrTooManyViewers):
		writeCampaignError(w, http.StatusTooManyRequests, codeTooManyViewers,
			"too many open dashboards for this campaign; close one and retry", nil, nil)
	case errors.Is(err, monitoring.ErrClosed):
		writeCampaignError(w, http.StatusServiceUnavailable, "unavailable", "the server is shutting down", nil, nil)
	case errors.Is(err, context.Canceled):
		h.logger.InfoContext(ctx, op+" cancelled by client", "request_id", middleware.GetReqID(ctx))
	default:
		h.logger.ErrorContext(ctx, op+" failed", "request_id", middleware.GetReqID(ctx), "error", err)
		writeCampaignError(w, http.StatusInternalServerError, "internal", "internal server error", nil, nil)
	}
}
