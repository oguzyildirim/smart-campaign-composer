package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
)

// recordTimeout bounds writing an outcome after the attempt's own context
// ended (shutdown), so the result of a finished send is not lost.
const recordTimeout = 5 * time.Second

// ProcessorConfig configures delivery attempts.
type ProcessorConfig struct {
	// Lease is how long a claim stays exclusive. It must exceed the rate
	// limiter wait plus the provider timeout, or a slow attempt loses its
	// claim (it then cannot record its outcome; the retry is harmless
	// because the idempotency key is reused).
	Lease  time.Duration
	Policy RetryPolicy
	// Rand returns a number in [0, 1) for backoff jitter; nil uses
	// math/rand/v2.
	Rand func() float64
	// Metrics receives attempt outcomes and rate-limit waits; nil
	// discards them.
	Metrics Metrics
}

// Processor performs delivery attempts. It is safe for concurrent use.
type Processor struct {
	store    Store
	limiter  RateLimiter
	provider Provider
	cfg      ProcessorConfig
	logger   *slog.Logger
}

// NewProcessor returns a Processor.
func NewProcessor(store Store, limiter RateLimiter, provider Provider, cfg ProcessorConfig, logger *slog.Logger) *Processor {
	if cfg.Rand == nil {
		cfg.Rand = rand.Float64
	}
	if cfg.Metrics == nil {
		cfg.Metrics = NopMetrics{}
	}
	return &Processor{store: store, limiter: limiter, provider: provider, cfg: cfg, logger: logger}
}

// Process performs one attempt of delivery id: claim, wait for the
// channel's rate limit, re-check consent and render the final content
// from current data, send, and record the outcome. A delivery that is not
// claimable is a duplicate message and is skipped. Process returns an
// error only when the claim or the outcome could not be read or written;
// the queue message should then be retried (a claimed delivery whose
// outcome was lost is recovered when its lease expires).
func (p *Processor) Process(ctx context.Context, id ID) error {
	job, ok, err := p.store.Claim(ctx, id, p.cfg.Lease)
	if err != nil {
		return fmt.Errorf("claim delivery: %w", err)
	}
	if !ok {
		p.logger.DebugContext(ctx, "delivery not claimable, skipping duplicate", "delivery_id", string(id))
		return nil
	}

	o := p.attempt(ctx, job)

	// The outcome must be stored even if ctx ended during the attempt.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	recorded, err := p.store.Record(rctx, job, o)
	if err != nil && !recorded {
		return fmt.Errorf("record delivery outcome: %w", err)
	}
	if recorded {
		p.cfg.Metrics.AttemptRecorded(job.Channel, o)
	}
	if err != nil {
		// The outcome is stored; only the campaign completion check failed,
		// and the maintenance sweep repeats it. Retrying the message would
		// find nothing to claim.
		p.logger.WarnContext(ctx, "delivery recorded, campaign completion check failed",
			"delivery_id", string(job.ID), "campaign_id", job.CampaignID, "error", err)
	}
	attrs := []any{
		"delivery_id", string(job.ID), "campaign_id", job.CampaignID, "channel", string(job.Channel),
		"attempt", job.Attempt, "status", string(o.Status), "error_code", o.ErrorCode,
	}
	switch {
	case !recorded:
		p.logger.WarnContext(ctx, "delivery claim lost before recording the outcome", attrs...)
	case o.Status == StatusSent:
		p.logger.DebugContext(ctx, "delivery sent", attrs...)
	case o.Status == StatusFailed:
		p.logger.WarnContext(ctx, "delivery failed", attrs...)
	default:
		p.logger.InfoContext(ctx, "delivery will be retried", append(attrs, "retry_in", o.RetryIn.String())...)
	}
	return nil
}

// attempt runs a claimed job up to and including the provider call.
func (p *Processor) attempt(ctx context.Context, job Job) Outcome {
	waitStart := time.Now()
	if err := p.limiter.Wait(ctx, job.Channel); err != nil {
		if ctx.Err() != nil {
			return release()
		}
		return p.retry(job, CodeInternal, 0)
	}
	// Only waits that obtained a token: cancelled waits would skew it.
	p.cfg.Metrics.RateLimitWaited(job.Channel, time.Since(waitStart))
	// Read consent and values after the rate-limit wait, as close to the
	// send as possible.
	r, err := p.store.Recipient(ctx, job)
	if err != nil {
		if ctx.Err() != nil {
			return release()
		}
		return p.retry(job, CodeInternal, 0)
	}
	if !r.Consented {
		return Outcome{Status: StatusFailed, ErrorCode: CodeConsentRevoked}
	}
	msg, errs := content.RenderMessage(r.Drafts, job.Channel, r.Values)
	if len(errs) > 0 {
		return Outcome{Status: StatusFailed, ErrorCode: CodeInvalidContent}
	}

	providerID, err := p.provider.Send(ctx, Message{
		IdempotencyKey: job.ID, Channel: job.Channel, To: r.Address, Subject: msg.Subject, Body: msg.Body,
	})
	if err == nil {
		return Outcome{Status: StatusSent, ProviderMessageID: providerID}
	}
	if ctx.Err() != nil {
		// Shutdown mid-send: the provider may or may not have the message.
		// Retrying reuses the idempotency key, so it cannot deliver twice.
		return release()
	}
	pe, ok := errors.AsType[*ProviderError](err)
	switch {
	case !ok:
		return p.retry(job, CodeInternal, 0)
	case !pe.Transient:
		return Outcome{Status: StatusFailed, ErrorCode: pe.Code}
	default:
		return p.retry(job, pe.Code, pe.RetryAfter)
	}
}

// retry schedules the next attempt, or fails the job when this was its
// last allowed attempt.
func (p *Processor) retry(job Job, code string, retryAfter time.Duration) Outcome {
	if job.Attempt >= job.MaxAttempts {
		return Outcome{Status: StatusFailed, ErrorCode: CodeRetriesExhausted}
	}
	return Outcome{Status: StatusRetrying, ErrorCode: code, RetryIn: p.cfg.Policy.Delay(job.Attempt, retryAfter, p.cfg.Rand())}
}

func release() Outcome {
	return Outcome{Status: StatusRetrying, ErrorCode: CodeInterrupted, Release: true}
}
