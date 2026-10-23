package delivery

import "time"

// RetryPolicy schedules retries of transient failures: exponential
// backoff with full jitter, where a provider's Retry-After is a lower
// bound. Every delay is capped at MaxDelay. The number of attempts is
// stored per delivery (max_attempts), not here.
type RetryPolicy struct {
	BaseDelay time.Duration
	Factor    int
	MaxDelay  time.Duration
}

// Ceiling is the backoff ceiling after the given attempt (1-based):
// BaseDelay·Factor^(attempt-1), capped at MaxDelay.
func (p RetryPolicy) Ceiling(attempt int) time.Duration {
	d := p.BaseDelay
	for i := 1; i < attempt && d < p.MaxDelay; i++ {
		d *= time.Duration(max(p.Factor, 1))
	}
	return min(d, p.MaxDelay)
}

// Backoff is the full-jitter delay after attempt: rnd (in [0, 1)) times
// the ceiling. Full jitter spreads retries of many deliveries that failed
// together, so they do not return to the provider in lockstep.
func (p RetryPolicy) Backoff(attempt int, rnd float64) time.Duration {
	return time.Duration(rnd * float64(p.Ceiling(attempt)))
}

// Delay is the wait before the next attempt: the backoff, or the
// provider's Retry-After when that is longer, capped at MaxDelay.
func (p RetryPolicy) Delay(attempt int, retryAfter time.Duration, rnd float64) time.Duration {
	return min(max(p.Backoff(attempt, rnd), retryAfter), p.MaxDelay)
}
