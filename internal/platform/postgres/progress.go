package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/delivery"
	"github.com/oguzyildirim/smart-campaign-composer/internal/monitoring"
)

// ProgressRepository implements monitoring.Source.
type ProgressRepository struct {
	pool *pgxpool.Pool
}

var _ monitoring.Source = (*ProgressRepository)(nil)

// NewProgressRepository returns a repository backed by pool.
func NewProgressRepository(pool *pgxpool.Pool) *ProgressRepository {
	return &ProgressRepository{pool: pool}
}

// progressQuery reads the campaign and its delivery aggregates in one
// statement, so status and counts come from the same snapshot. now() is
// fixed for the statement (unlike clock_timestamp()), so the recent-sends
// window and generated_at agree. It uses
// deliveries_campaign_status_idx. The campaign row is repeated per
// aggregate; without deliveries, one row has NULL aggregate columns.
const progressQuery = `SELECT c.status, c.channels, c.scheduled_at, now(), g.channel, g.status, g.code, g.n, g.recent
FROM campaigns c
LEFT JOIN LATERAL (
	SELECT d.channel, d.status,
		CASE WHEN d.status = 'failed' THEN coalesce(d.last_error_code, '') ELSE '' END AS code,
		count(*) AS n,
		count(*) FILTER (WHERE d.sent_at >= now() - $2 * interval '1 microsecond') AS recent
	FROM deliveries d
	WHERE d.campaign_id = c.id
	GROUP BY 1, 2, 3
) g ON true
WHERE c.id = $1`

// Progress implements monitoring.Source.
func (r *ProgressRepository) Progress(ctx context.Context, id campaign.ID, window time.Duration) (monitoring.Snapshot, error) {
	var (
		found    bool
		status   string
		channels []string
		schedAt  *time.Time
		at       time.Time
		aggs     []monitoring.Row
	)
	rows, err := r.pool.Query(ctx, progressQuery, string(id), window.Microseconds())
	if err != nil {
		return monitoring.Snapshot{}, fmt.Errorf("read campaign progress: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			ch, st, code *string
			n, recent    *int64
		)
		if err := rows.Scan(&status, &channels, &schedAt, &at, &ch, &st, &code, &n, &recent); err != nil {
			return monitoring.Snapshot{}, fmt.Errorf("read campaign progress: %w", err)
		}
		found = true
		if ch == nil {
			continue // no deliveries
		}
		aggs = append(aggs, monitoring.Row{
			Channel: content.Channel(*ch), Status: delivery.Status(*st), ErrorCode: *code, Count: *n, RecentSent: *recent,
		})
	}
	if err := rows.Err(); err != nil {
		return monitoring.Snapshot{}, fmt.Errorf("read campaign progress: %w", err)
	}
	if !found {
		return monitoring.Snapshot{}, campaign.ErrNotFound
	}
	selected, err := content.ParseChannels(channels)
	if err != nil {
		return monitoring.Snapshot{}, fmt.Errorf("stored channels of campaign %s: %w", id, err)
	}
	snap := monitoring.Assemble(id, campaign.Status(status), selected, aggs, window, at)
	if schedAt != nil {
		snap.ScheduledAt = *schedAt
	}
	return snap, nil
}
