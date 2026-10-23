//go:build integration

package postgres

import (
	"context"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
)

// TakeAt is Take that also reports the database time of the attempt, so
// tests can check the sliding window on the limiter's own clock.
func (b *TokenBucket) TakeAt(ctx context.Context, c content.Channel) (granted bool, at time.Time, err error) {
	var avail float64
	err = b.pool.QueryRow(ctx, `WITH t AS (`+takeQuery+`, new.updated_at) SELECT * FROM t`, string(c), b.burst, b.rate).
		Scan(&avail, &at)
	return avail >= 1, at, err
}
