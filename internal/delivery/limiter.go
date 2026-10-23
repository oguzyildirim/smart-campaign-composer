package delivery

import (
	"context"
	"fmt"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
)

// TokenBucket is a rate limit shared by every worker. Take removes one
// token for channel c if one is available and returns 0, or returns how
// long until one will be.
type TokenBucket interface {
	Take(ctx context.Context, c content.Channel) (wait time.Duration, err error)
}

// Limiter implements RateLimiter on a shared TokenBucket. Within one
// process only one caller per channel asks the bucket at a time, so N
// waiting goroutines cost one bucket request, not N, each time a token
// frees up.
type Limiter struct {
	bucket TokenBucket
	turns  map[content.Channel]chan struct{}
}

var _ RateLimiter = (*Limiter)(nil)

// NewLimiter returns a Limiter for every supported channel.
func NewLimiter(bucket TokenBucket) *Limiter {
	return &Limiter{bucket: bucket, turns: map[content.Channel]chan struct{}{
		content.ChannelSMS:   make(chan struct{}, 1),
		content.ChannelEmail: make(chan struct{}, 1),
	}}
}

// Wait implements RateLimiter.
func (l *Limiter) Wait(ctx context.Context, c content.Channel) error {
	turn, ok := l.turns[c]
	if !ok {
		return fmt.Errorf("rate limit: unknown channel %q", c)
	}
	select {
	case turn <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-turn }()

	for {
		wait, err := l.bucket.Take(ctx, c)
		if err != nil {
			return fmt.Errorf("rate limit %s: %w", c, err)
		}
		if wait <= 0 {
			return nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
