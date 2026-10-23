package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

// queryer is the read side shared by the pool and a transaction.
type queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// CampaignStore implements campaign.Store.
type CampaignStore struct {
	pool *pgxpool.Pool
}

var _ campaign.Store = (*CampaignStore)(nil)

// NewCampaignStore returns a store backed by pool.
func NewCampaignStore(pool *pgxpool.Pool) *CampaignStore {
	return &CampaignStore{pool: pool}
}

const campaignColumns = `id::text, brief, status, segment_rule, segment_explanation, unsupported_criteria,
	audience_total, audience_sms, audience_email, channels, sms_body, email_subject, email_body,
	version, content_hash, generation_token, generation_expires_at, approved_by, approved_at,
	created_at, updated_at`

// Create implements campaign.Store.
func (s *CampaignStore) Create(ctx context.Context, c campaign.Campaign, events []campaign.Event) (campaign.Campaign, error) {
	rule, err := ruleJSON(c)
	if err != nil {
		return campaign.Campaign{}, err
	}
	unsupported, err := json.Marshal(nonNil(c.UnsupportedCriteria))
	if err != nil {
		return campaign.Campaign{}, fmt.Errorf("encode unsupported criteria: %w", err)
	}
	var created campaign.Campaign
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `INSERT INTO campaigns (brief, status, segment_rule, segment_explanation,
				unsupported_criteria, audience_total, audience_sms, audience_email, channels,
				sms_body, email_subject, email_body, version, content_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			RETURNING `+campaignColumns,
			c.Brief, string(c.Status), rule, c.Explanation, unsupported,
			c.Counts.Total, c.Counts.SMS, c.Counts.Email, channelNames(c.Channels),
			c.Drafts.SMS.Body, c.Drafts.Email.Subject, c.Drafts.Email.Body, c.Version, c.ContentHash)
		var err error
		if created, err = scanCampaign(row); err != nil {
			return fmt.Errorf("insert campaign: %w", err)
		}
		return insertEvents(ctx, tx, created.ID, events)
	})
	if err != nil {
		return campaign.Campaign{}, err
	}
	return created, nil
}

// Get implements campaign.Store.
func (s *CampaignStore) Get(ctx context.Context, id campaign.ID) (campaign.Campaign, error) {
	c, err := scanCampaign(s.pool.QueryRow(ctx, `SELECT `+campaignColumns+` FROM campaigns WHERE id = $1`, string(id)))
	if err != nil {
		return campaign.Campaign{}, fmt.Errorf("get campaign: %w", err)
	}
	return c, nil
}

// List implements campaign.Store.
func (s *CampaignStore) List(ctx context.Context, limit int) ([]campaign.Campaign, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+campaignColumns+` FROM campaigns ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list campaigns: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (campaign.Campaign, error) { return scanCampaign(row) })
	if err != nil {
		return nil, fmt.Errorf("list campaigns: %w", err)
	}
	return out, nil
}

// Update implements campaign.Store. The row lock (FOR UPDATE) serializes
// concurrent updates of one campaign; the version predicate on the write
// is a second guard that turns any lost update into ErrConflict.
func (s *CampaignStore) Update(ctx context.Context, id campaign.ID, fn campaign.UpdateFunc) (campaign.Campaign, error) {
	var result campaign.Campaign
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanCampaign(tx.QueryRow(ctx, `SELECT `+campaignColumns+` FROM campaigns WHERE id = $1 FOR UPDATE`, string(id)))
		if err != nil {
			return fmt.Errorf("lock campaign: %w", err)
		}
		w, err := fn(ctx, audienceInspector{q: tx}, cur)
		if err != nil {
			return err
		}
		if w == nil {
			result = cur
			return nil
		}
		if result, err = writeCampaign(ctx, tx, cur.Version, w.Campaign); err != nil {
			return err
		}
		return insertEvents(ctx, tx, id, w.Events)
	})
	if err != nil {
		return campaign.Campaign{}, err
	}
	return result, nil
}

// writeCampaign persists the mutable columns. The audience proposal and
// brief are fixed at creation and never rewritten.
func writeCampaign(ctx context.Context, tx pgx.Tx, lockedVersion int, c campaign.Campaign) (campaign.Campaign, error) {
	row := tx.QueryRow(ctx, `UPDATE campaigns SET
			status = $3, channels = $4, sms_body = $5, email_subject = $6, email_body = $7,
			version = $8, content_hash = $9, generation_token = $10, generation_expires_at = $11,
			approved_by = $12, approved_at = $13, updated_at = now()
		WHERE id = $1 AND version = $2
		RETURNING `+campaignColumns,
		string(c.ID), lockedVersion, string(c.Status), channelNames(c.Channels),
		c.Drafts.SMS.Body, c.Drafts.Email.Subject, c.Drafts.Email.Body, c.Version, c.ContentHash,
		nullString(c.GenerationToken), nullTime(c.GenerationExpiresAt), nullString(c.ApprovedBy), nullTime(c.ApprovedAt))
	out, err := scanCampaign(row)
	if errors.Is(err, campaign.ErrNotFound) {
		return campaign.Campaign{}, campaign.ErrConflict
	}
	if err != nil {
		return campaign.Campaign{}, fmt.Errorf("update campaign: %w", err)
	}
	return out, nil
}

func insertEvents(ctx context.Context, tx pgx.Tx, id campaign.ID, events []campaign.Event) error {
	for _, ev := range events {
		if _, err := tx.Exec(ctx, `INSERT INTO campaign_events (campaign_id, action, actor, from_status, to_status, version)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			string(id), ev.Action, ev.Actor, nullString(string(ev.From)), string(ev.To), ev.Version); err != nil {
			return fmt.Errorf("insert campaign event: %w", err)
		}
	}
	return nil
}

// scanCampaign reads one campaignColumns row. The stored rule goes back
// through the segment validator, so a corrupted rule is an error rather
// than an unchecked value. pgx.ErrNoRows becomes campaign.ErrNotFound.
func scanCampaign(row pgx.Row) (campaign.Campaign, error) {
	var (
		c                       campaign.Campaign
		id, status              string
		rule, unsupported       []byte
		channels                []string
		genToken, approvedBy    *string
		genExpires, approvedAtP *time.Time
	)
	err := row.Scan(&id, &c.Brief, &status, &rule, &c.Explanation, &unsupported,
		&c.Counts.Total, &c.Counts.SMS, &c.Counts.Email, &channels,
		&c.Drafts.SMS.Body, &c.Drafts.Email.Subject, &c.Drafts.Email.Body,
		&c.Version, &c.ContentHash, &genToken, &genExpires, &approvedBy, &approvedAtP,
		&c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return campaign.Campaign{}, campaign.ErrNotFound
	}
	if err != nil {
		return campaign.Campaign{}, err
	}
	c.ID, c.Status = campaign.ID(id), campaign.Status(status)
	if rule != nil {
		if c.Rule, err = segment.Parse(rule); err != nil {
			return campaign.Campaign{}, fmt.Errorf("stored segment rule of campaign %s: %w", id, err)
		}
	}
	if err := json.Unmarshal(unsupported, &c.UnsupportedCriteria); err != nil {
		return campaign.Campaign{}, fmt.Errorf("stored unsupported criteria of campaign %s: %w", id, err)
	}
	if c.Channels, err = content.ParseChannels(channels); err != nil {
		return campaign.Campaign{}, fmt.Errorf("stored channels of campaign %s: %w", id, err)
	}
	if genToken != nil && genExpires != nil {
		c.GenerationToken, c.GenerationExpiresAt = *genToken, *genExpires
	}
	if approvedBy != nil && approvedAtP != nil {
		c.ApprovedBy, c.ApprovedAt = *approvedBy, *approvedAtP
	}
	return c, nil
}

// ruleJSON encodes the rule, or NULL for a campaign without one.
func ruleJSON(c campaign.Campaign) ([]byte, error) {
	if len(c.Rule.Conditions) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(c.Rule)
	if err != nil {
		return nil, fmt.Errorf("encode segment rule: %w", err)
	}
	return b, nil
}

func channelNames(cs []content.Channel) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
