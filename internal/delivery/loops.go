package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/strictjson"
)

// maxIdleBackoff caps the pause after a failed loop iteration.
const maxIdleBackoff = 5 * time.Second

// JobPayload is the queue message for one delivery. It carries only the
// delivery id; all state is read from the database when processing.
type JobPayload struct {
	DeliveryID string `json:"delivery_id"`
}

// DecodeJob parses a queue message strictly.
func DecodeJob(payload []byte) (ID, error) {
	var p JobPayload
	if err := strictjson.Decode(payload, &p); err != nil {
		return "", fmt.Errorf("decode delivery job: %w", err)
	}
	return ParseID(p.DeliveryID)
}

// EncodeJob returns the queue message for delivery id.
func EncodeJob(id ID) []byte {
	b, _ := json.Marshal(JobPayload{DeliveryID: string(id)}) // cannot fail for a string field
	return b
}

// Handle processes one queue message. A malformed message is logged and
// dropped (returns nil), since retrying it cannot succeed; an error means
// the message must be retried.
func (p *Processor) Handle(ctx context.Context, payload []byte) error {
	id, err := DecodeJob(payload)
	if err != nil {
		p.logger.ErrorContext(ctx, "dropping malformed delivery job", "error", err)
		return nil
	}
	return p.Process(ctx, id)
}

// OutboxMessage is a queue message waiting in the transactional outbox.
type OutboxMessage struct {
	ID      int64
	Topic   string
	Key     string
	Payload []byte
}

// Outbox is the transactional outbox.
type Outbox interface {
	// PublishPending locks up to limit unpublished messages (skipping rows
	// other relays hold), passes them to publish, and marks them published
	// if publish succeeds. It returns the number published.
	PublishPending(ctx context.Context, limit int, publish func(context.Context, []OutboxMessage) error) (int, error)
}

// Publisher writes messages to the queue durably (acknowledged by every
// in-sync replica) before returning.
type Publisher interface {
	Publish(ctx context.Context, msgs []OutboxMessage) error
}

// RelayConfig paces the outbox relay.
type RelayConfig struct {
	BatchSize    int
	PollInterval time.Duration
}

// RunRelay moves outbox messages to the queue until ctx ends. A crash
// between publishing and marking rows republishes them: delivery is
// at least once, and consumers absorb duplicates by claiming.
func RunRelay(ctx context.Context, outbox Outbox, pub Publisher, cfg RelayConfig, logger *slog.Logger) error {
	backoff := cfg.PollInterval
	for {
		n, err := outbox.PublishPending(ctx, cfg.BatchSize, pub.Publish)
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil:
			logger.WarnContext(ctx, "outbox relay failed; retrying", "error", err, "retry_in", backoff.String())
		case n == cfg.BatchSize:
			backoff = cfg.PollInterval
			continue // more may be waiting
		default:
			backoff = cfg.PollInterval
			if n > 0 {
				logger.DebugContext(ctx, "outbox published", "messages", n)
			}
		}
		if !sleep(ctx, backoff) {
			return nil
		}
		if err != nil {
			backoff = min(backoff*2, maxIdleBackoff)
		}
	}
}

// Maintenance is the periodic upkeep of delivery state.
type Maintenance interface {
	// RecoverExpiredLeases returns processing deliveries whose lease ran
	// out (their worker died or stalled) to retrying, or fails them when
	// no attempt is left. The lost attempt stays counted.
	RecoverExpiredLeases(ctx context.Context, limit int) (int, error)
	// RequeueDue moves due retrying deliveries back to queued and writes
	// their queue messages to the outbox, in one transaction.
	RequeueDue(ctx context.Context, limit int) (int, error)
	// CompleteFinishedCampaigns completes every sending campaign whose
	// deliveries are all terminal.
	CompleteFinishedCampaigns(ctx context.Context) (int, error)
	// PurgePublishedOutbox deletes outbox messages published longer ago
	// than olderThan.
	PurgePublishedOutbox(ctx context.Context, olderThan time.Duration) (int, error)
	// PendingOutbox counts unpublished outbox messages.
	PendingOutbox(ctx context.Context) (int, error)
}

// Releaser releases scheduled campaigns that are due, creating their
// deliveries. *campaign.Service implements it.
type Releaser interface {
	// ReleaseDue handles up to limit due campaigns and returns how many
	// changed.
	ReleaseDue(ctx context.Context, limit int) (int, error)
}

// MaintenanceConfig paces RunMaintenance.
type MaintenanceConfig struct {
	Interval  time.Duration
	BatchSize int
	// OutboxRetention keeps published outbox rows this long for
	// inspection.
	OutboxRetention time.Duration
	// Metrics receives recovered leases and the outbox backlog; nil
	// discards them.
	Metrics Metrics
	// Releaser, when set, releases due scheduled campaigns first on every
	// tick, so a release is at most one Interval late while workers run.
	Releaser Releaser
}

// purgeEvery runs the outbox purge on every n-th maintenance tick.
const purgeEvery = 60

// RunMaintenance runs the release of due scheduled campaigns, lease
// recovery, the retry scheduler, and campaign completion every interval
// until ctx ends. Completion here is a safety
// net: workers also complete a campaign when they finish its last
// delivery.
func RunMaintenance(ctx context.Context, m Maintenance, cfg MaintenanceConfig, logger *slog.Logger) error {
	if cfg.Metrics == nil {
		cfg.Metrics = NopMetrics{}
	}
	for tick := 0; ; tick++ {
		maintain(ctx, m, cfg, tick%purgeEvery == 0, logger)
		if !sleep(ctx, cfg.Interval) {
			return nil
		}
	}
}

func maintain(ctx context.Context, m Maintenance, cfg MaintenanceConfig, purge bool, logger *slog.Logger) {
	if cfg.Releaser != nil {
		drain(ctx, cfg.BatchSize, cfg.Releaser.ReleaseDue, logger, "release scheduled campaigns")
	}
	if n, err := m.RecoverExpiredLeases(ctx, cfg.BatchSize); err != nil {
		logMaintenanceError(ctx, logger, "recover expired leases", err)
	} else if n > 0 {
		cfg.Metrics.LeasesRecovered(n)
		logger.InfoContext(ctx, "recovered deliveries with expired leases", "deliveries", n)
	}
	drain(ctx, cfg.BatchSize, m.RequeueDue, logger, "requeue due retries")
	if n, err := m.CompleteFinishedCampaigns(ctx); err != nil {
		logMaintenanceError(ctx, logger, "complete finished campaigns", err)
	} else if n > 0 {
		logger.InfoContext(ctx, "completed campaigns", "campaigns", n)
	}
	if n, err := m.PendingOutbox(ctx); err != nil {
		logMaintenanceError(ctx, logger, "count pending outbox", err)
	} else {
		cfg.Metrics.OutboxPending(n)
	}
	if purge {
		if _, err := m.PurgePublishedOutbox(ctx, cfg.OutboxRetention); err != nil {
			logMaintenanceError(ctx, logger, "purge published outbox", err)
		}
	}
}

// drain calls step with batch until it handles less than a full batch or
// fails.
func drain(ctx context.Context, batch int, step func(context.Context, int) (int, error), logger *slog.Logger, task string) {
	for {
		n, err := step(ctx, batch)
		if err != nil {
			logMaintenanceError(ctx, logger, task, err)
		}
		if err != nil || n < batch {
			return
		}
	}
}

func logMaintenanceError(ctx context.Context, logger *slog.Logger, task string, err error) {
	if ctx.Err() == nil {
		logger.WarnContext(ctx, "delivery maintenance failed", "task", task, "error", err)
	}
}

// sleep waits for d or until ctx ends, reporting whether ctx is still live.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
