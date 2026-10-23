package delivery

import (
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
)

// Metrics receives delivery events for operational monitoring.
// Implementations must be safe for concurrent use and must only turn the
// bounded values passed here (channels, statuses, error codes) into
// labels, never delivery, campaign, or recipient identifiers.
type Metrics interface {
	// AttemptRecorded reports a stored attempt outcome.
	AttemptRecorded(c content.Channel, o Outcome)
	// RateLimitWaited reports how long an attempt waited for its token.
	RateLimitWaited(c content.Channel, d time.Duration)
	// LeasesRecovered reports deliveries returned after an expired lease.
	LeasesRecovered(n int)
	// OutboxPending reports the number of unpublished outbox messages.
	OutboxPending(n int)
}

// NopMetrics discards every event.
type NopMetrics struct{}

var _ Metrics = NopMetrics{}

// AttemptRecorded implements Metrics.
func (NopMetrics) AttemptRecorded(content.Channel, Outcome) {}

// RateLimitWaited implements Metrics.
func (NopMetrics) RateLimitWaited(content.Channel, time.Duration) {}

// LeasesRecovered implements Metrics.
func (NopMetrics) LeasesRecovered(int) {}

// OutboxPending implements Metrics.
func (NopMetrics) OutboxPending(int) {}
