package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

// consentFilter is true when user u granted consent for the channel bound
// to the given placeholder. A missing consent row counts as no consent.
const consentFilter = `EXISTS (SELECT 1 FROM channel_consents cc
	WHERE cc.user_id = u.id AND cc.channel = %s AND cc.granted)`

// Channel names as stored in channel_consents.channel.
const (
	channelSMS   = "sms"
	channelEmail = "email"
)

// AudienceRepository counts segment audiences. It applies channel consent
// itself, so no caller can produce a per-channel count that includes users
// without consent.
type AudienceRepository struct {
	pool *pgxpool.Pool
}

// NewAudienceRepository returns a repository backed by pool.
func NewAudienceRepository(pool *pgxpool.Pool) *AudienceRepository {
	return &AudienceRepository{pool: pool}
}

// CountAudience implements segment.AudienceCounter.
func (r *AudienceRepository) CountAudience(ctx context.Context, p segment.Predicate) (segment.Counts, error) {
	query, args := countQuery(p)
	var c segment.Counts
	if err := r.pool.QueryRow(ctx, query, args...).Scan(&c.Total, &c.SMS, &c.Email); err != nil {
		return segment.Counts{}, fmt.Errorf("count audience: %w", err)
	}
	return c, nil
}

// countQuery wraps the segment predicate in a single aggregate. The
// channel names are bound after the predicate's own arguments.
func countQuery(p segment.Predicate) (string, []any) {
	n := len(p.Args)
	smsPlaceholder := fmt.Sprintf("$%d", n+1)
	emailPlaceholder := fmt.Sprintf("$%d", n+2)

	query := `SELECT count(*),
	count(*) FILTER (WHERE ` + fmt.Sprintf(consentFilter, smsPlaceholder) + `),
	count(*) FILTER (WHERE ` + fmt.Sprintf(consentFilter, emailPlaceholder) + `)
FROM users u
WHERE ` + p.SQL

	args := make([]any, 0, n+2)
	args = append(args, p.Args...)
	args = append(args, channelSMS, channelEmail)
	return query, args
}
