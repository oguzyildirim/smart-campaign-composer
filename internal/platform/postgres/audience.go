package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
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

// variableColumns maps personalization variables to user columns. Only
// these columns are ever read for a recipient.
var variableColumns = map[content.Variable]string{
	content.VarFirstName: "u.first_name",
	content.VarCity:      "u.city",
}

// InspectAudience implements campaign.AudienceInspector outside a
// transaction.
func (r *AudienceRepository) InspectAudience(ctx context.Context, p segment.Predicate, ch content.Channel, samples int) (campaign.ChannelAudience, error) {
	return audienceInspector{q: r.pool}.InspectAudience(ctx, p, ch, samples)
}

// audienceInspector reads a channel's consented audience through q, which
// may be a transaction.
type audienceInspector struct {
	q queryer
}

// InspectAudience implements campaign.AudienceInspector: the consented
// recipient count, the longest value of each variable, and up to samples
// recipients: the one with the longest first name plus the lowest ids,
// ordered longest first name first.
func (a audienceInspector) InspectAudience(ctx context.Context, p segment.Predicate, ch content.Channel, samples int) (campaign.ChannelAudience, error) {
	if ch != content.ChannelSMS && ch != content.ChannelEmail {
		return campaign.ChannelAudience{}, fmt.Errorf("inspect audience: unknown channel %q", ch)
	}
	n := len(p.Args)
	where := `(` + p.SQL + `) AND ` + fmt.Sprintf(consentFilter, fmt.Sprintf("$%d", n+1))
	args := append(append(make([]any, 0, n+2), p.Args...), string(ch))

	out := campaign.ChannelAudience{Longest: map[content.Variable]int{}}
	var longestName, longestCity int
	err := a.q.QueryRow(ctx, `SELECT count(*),
		coalesce(max(char_length(`+variableColumns[content.VarFirstName]+`)), 0),
		coalesce(max(char_length(`+variableColumns[content.VarCity]+`)), 0)
	FROM users u WHERE `+where, args...).Scan(&out.Recipients, &longestName, &longestCity)
	if err != nil {
		return campaign.ChannelAudience{}, fmt.Errorf("inspect %s audience: %w", ch, err)
	}
	out.Longest[content.VarFirstName], out.Longest[content.VarCity] = longestName, longestCity
	if samples <= 0 || out.Recipients == 0 {
		return out, nil
	}

	// The longest-name recipient sorts first, so the outer limit keeps it.
	args = append(args, samples)
	limit := fmt.Sprintf("$%d", n+2)
	cols := `u.id, ` + variableColumns[content.VarFirstName] + `, ` + variableColumns[content.VarCity]
	rows, err := a.q.Query(ctx, `SELECT id, first_name, city FROM (
		(SELECT `+cols+` FROM users u WHERE `+where+` ORDER BY char_length(u.first_name) DESC, u.id LIMIT 1)
		UNION
		(SELECT `+cols+` FROM users u WHERE `+where+` ORDER BY u.id LIMIT `+limit+`)
	) s ORDER BY char_length(first_name) DESC, id LIMIT `+limit, args...)
	if err != nil {
		return campaign.ChannelAudience{}, fmt.Errorf("sample %s audience: %w", ch, err)
	}
	out.Samples, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (campaign.Recipient, error) {
		var (
			r               campaign.Recipient
			firstName, city string
		)
		if err := row.Scan(&r.UserID, &firstName, &city); err != nil {
			return r, err
		}
		r.Values = map[content.Variable]string{content.VarFirstName: firstName, content.VarCity: city}
		return r, nil
	})
	if err != nil {
		return campaign.ChannelAudience{}, fmt.Errorf("sample %s audience: %w", ch, err)
	}
	return out, nil
}
