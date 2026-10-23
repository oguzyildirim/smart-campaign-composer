package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/delivery"
)

// Every time comparison and timestamp in this file uses the database
// clock (clock_timestamp()), so leases, retry times, and rate-limit
// refills never depend on worker clocks.

// materializeDeliveries applies an approval's delivery plan inside the
// approval transaction: one delivery per user matching the predicate who
// granted consent for the channel, and one outbox message per delivery
// created. ON CONFLICT keeps existing rows, so re-applying creates nothing.
func materializeDeliveries(ctx context.Context, tx pgx.Tx, id campaign.ID, plan campaign.DeliveryPlan) error {
	n := len(plan.Predicate.Args)
	ph := func(i int) string { return fmt.Sprintf("$%d", n+i) }
	query := `WITH created AS (
		INSERT INTO deliveries (campaign_id, user_id, channel, max_attempts)
		SELECT ` + ph(1) + `, u.id, ` + ph(2) + `, ` + ph(3) + ` FROM users u
		WHERE (` + plan.Predicate.SQL + `) AND ` + fmt.Sprintf(consentFilter, ph(2)) + `
		ON CONFLICT (campaign_id, user_id, channel) DO NOTHING
		RETURNING id, channel
	)
	INSERT INTO outbox (topic, key, payload)
	SELECT ` + ph(4) + ` || channel, id::text, jsonb_build_object('delivery_id', id) FROM created`
	for _, ch := range plan.Channels {
		if ch != content.ChannelSMS && ch != content.ChannelEmail {
			return fmt.Errorf("materialize deliveries: unknown channel %q", ch)
		}
		args := append(append(make([]any, 0, n+4), plan.Predicate.Args...),
			string(id), string(ch), plan.MaxAttempts, delivery.TopicPrefix)
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			return fmt.Errorf("materialize %s deliveries: %w", ch, err)
		}
	}
	return nil
}

// DeliveryStore implements delivery.Store, delivery.Maintenance, and
// delivery.Outbox.
type DeliveryStore struct {
	pool *pgxpool.Pool
}

var (
	_ delivery.Store       = (*DeliveryStore)(nil)
	_ delivery.Maintenance = (*DeliveryStore)(nil)
	_ delivery.Outbox      = (*DeliveryStore)(nil)
)

// NewDeliveryStore returns a store backed by pool.
func NewDeliveryStore(pool *pgxpool.Pool) *DeliveryStore {
	return &DeliveryStore{pool: pool}
}

// claimQuery claims a delivery and, for the campaign's first claim, moves
// the campaign from approved to sending with an audit event. Rows another
// transaction is updating are waited for, then re-checked, so exactly one
// concurrent claimer wins.
const claimQuery = `WITH claimed AS (
	UPDATE deliveries SET status = 'processing', attempts = attempts + 1,
		locked_until = clock_timestamp() + $2 * interval '1 microsecond', updated_at = clock_timestamp()
	WHERE id = $1 AND status IN ('queued', 'retrying') AND next_attempt_at <= clock_timestamp()
		AND attempts < max_attempts
	RETURNING id, campaign_id, user_id, channel, attempts, max_attempts
), started AS (
	UPDATE campaigns c SET status = 'sending', version = c.version + 1, updated_at = now()
	FROM claimed WHERE c.id = claimed.campaign_id AND c.status = 'approved'
	RETURNING c.id, c.version
), started_event AS (
	INSERT INTO campaign_events (campaign_id, action, actor, from_status, to_status, version)
	SELECT id, $3, $4, 'approved', 'sending', version FROM started
)
SELECT id::text, campaign_id::text, user_id, channel, attempts, max_attempts FROM claimed`

// Claim implements delivery.Store.
func (s *DeliveryStore) Claim(ctx context.Context, id delivery.ID, lease time.Duration) (delivery.Job, bool, error) {
	var (
		job     delivery.Job
		jid, ch string
	)
	err := s.pool.QueryRow(ctx, claimQuery, string(id), lease.Microseconds(),
		campaign.ActionSendingStarted, campaign.ActorSystem).
		Scan(&jid, &job.CampaignID, &job.UserID, &ch, &job.Attempt, &job.MaxAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return delivery.Job{}, false, nil
	}
	if err != nil {
		return delivery.Job{}, false, fmt.Errorf("claim delivery: %w", err)
	}
	job.ID, job.Channel = delivery.ID(jid), content.Channel(ch)
	return job, true, nil
}

// Recipient implements delivery.Store: current consent, the address for
// the channel, the personalization columns, and the approved content.
func (s *DeliveryStore) Recipient(ctx context.Context, job delivery.Job) (delivery.Recipient, error) {
	var (
		r                     delivery.Recipient
		phone, email          string
		firstName, city, chID string
	)
	err := s.pool.QueryRow(ctx, `SELECT d.channel, u.phone, u.email, `+variableColumns[content.VarFirstName]+`, `+
		variableColumns[content.VarCity]+`, c.sms_body, c.email_subject, c.email_body,
		`+fmt.Sprintf(consentFilter, "d.channel")+`
	FROM deliveries d
	JOIN users u ON u.id = d.user_id
	JOIN campaigns c ON c.id = d.campaign_id
	WHERE d.id = $1`, string(job.ID)).
		Scan(&chID, &phone, &email, &firstName, &city, &r.Drafts.SMS.Body, &r.Drafts.Email.Subject, &r.Drafts.Email.Body, &r.Consented)
	if err != nil {
		return delivery.Recipient{}, fmt.Errorf("read delivery recipient: %w", err)
	}
	switch content.Channel(chID) {
	case content.ChannelSMS:
		r.Address = phone
	case content.ChannelEmail:
		r.Address = email
	default:
		return delivery.Recipient{}, fmt.Errorf("read delivery recipient: unknown channel %q", chID)
	}
	r.Values = map[content.Variable]string{content.VarFirstName: firstName, content.VarCity: city}
	return r, nil
}

// completeQuery completes approved or sending campaigns without unfinished
// deliveries, with an audit event; $1 restricts it to one campaign.
const completeQuery = `WITH done AS (
	UPDATE campaigns c SET status = 'completed', version = c.version + 1, updated_at = now()
	WHERE ($1::uuid IS NULL OR c.id = $1) AND c.status IN ('approved', 'sending')
		AND NOT EXISTS (SELECT 1 FROM deliveries d
			WHERE d.campaign_id = c.id AND d.status NOT IN ('sent', 'failed'))
	RETURNING c.id, old.status AS from_status, c.version
)
INSERT INTO campaign_events (campaign_id, action, actor, from_status, to_status, version)
SELECT id, $2, $3, from_status, 'completed', version FROM done`

// Record implements delivery.Store. The status and attempt predicate is
// the fence: a worker whose lease expired and whose delivery was claimed
// again cannot overwrite the newer attempt.
func (s *DeliveryStore) Record(ctx context.Context, job delivery.Job, o delivery.Outcome) (bool, error) {
	release := 0
	if o.Release {
		release = 1
	}
	tag, err := s.pool.Exec(ctx, `UPDATE deliveries SET
			status = $3,
			last_error_code = $4,
			provider_message_id = $5,
			sent_at = CASE WHEN $3 = 'sent' THEN clock_timestamp() END,
			next_attempt_at = CASE WHEN $3 = 'retrying'
				THEN clock_timestamp() + $6 * interval '1 microsecond' ELSE next_attempt_at END,
			attempts = attempts - $7,
			locked_until = NULL,
			updated_at = clock_timestamp()
		WHERE id = $1 AND status = 'processing' AND attempts = $2`,
		string(job.ID), job.Attempt, string(o.Status), nullString(o.ErrorCode), nullString(o.ProviderMessageID),
		o.RetryIn.Microseconds(), release)
	if err != nil {
		return false, fmt.Errorf("record delivery outcome: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if o.Status.Terminal() {
		if _, err := s.pool.Exec(ctx, completeQuery, job.CampaignID, campaign.ActionCompleted, campaign.ActorSystem); err != nil {
			return true, fmt.Errorf("complete campaign: %w", err)
		}
	}
	return true, nil
}

// RecoverExpiredLeases implements delivery.Maintenance.
func (s *DeliveryStore) RecoverExpiredLeases(ctx context.Context, limit int) (int, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE deliveries SET
			status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'retrying' END,
			last_error_code = CASE WHEN attempts >= max_attempts THEN $2 ELSE $3 END,
			next_attempt_at = clock_timestamp(), locked_until = NULL, updated_at = clock_timestamp()
		WHERE id IN (SELECT id FROM deliveries
			WHERE status = 'processing' AND locked_until < clock_timestamp()
			ORDER BY locked_until LIMIT $1 FOR UPDATE SKIP LOCKED)`,
		limit, delivery.CodeRetriesExhausted, delivery.CodeLeaseExpired)
	if err != nil {
		return 0, fmt.Errorf("recover expired leases: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// RequeueDue implements delivery.Maintenance.
func (s *DeliveryStore) RequeueDue(ctx context.Context, limit int) (int, error) {
	tag, err := s.pool.Exec(ctx, `WITH due AS (
			SELECT id FROM deliveries WHERE status = 'retrying' AND next_attempt_at <= clock_timestamp()
			ORDER BY next_attempt_at LIMIT $1 FOR UPDATE SKIP LOCKED
		), requeued AS (
			UPDATE deliveries d SET status = 'queued', updated_at = clock_timestamp()
			FROM due WHERE d.id = due.id
			RETURNING d.id, d.channel
		)
		INSERT INTO outbox (topic, key, payload)
		SELECT $2 || channel, id::text, jsonb_build_object('delivery_id', id) FROM requeued`,
		limit, delivery.TopicPrefix)
	if err != nil {
		return 0, fmt.Errorf("requeue due retries: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// CompleteFinishedCampaigns implements delivery.Maintenance.
func (s *DeliveryStore) CompleteFinishedCampaigns(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, completeQuery, nil, campaign.ActionCompleted, campaign.ActorSystem)
	if err != nil {
		return 0, fmt.Errorf("complete finished campaigns: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// PendingOutbox implements delivery.Maintenance. It uses
// outbox_unpublished_idx.
func (s *DeliveryStore) PendingOutbox(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count pending outbox: %w", err)
	}
	return n, nil
}

// PurgePublishedOutbox implements delivery.Maintenance.
func (s *DeliveryStore) PurgePublishedOutbox(ctx context.Context, olderThan time.Duration) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM outbox
		WHERE published_at < clock_timestamp() - $1 * interval '1 microsecond'`, olderThan.Microseconds())
	if err != nil {
		return 0, fmt.Errorf("purge published outbox: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// PublishPending implements delivery.Outbox. The rows stay locked while
// publish runs; if marking them fails after a successful publish, they are
// published again later.
func (s *DeliveryStore) PublishPending(ctx context.Context, limit int, publish func(context.Context, []delivery.OutboxMessage) error) (int, error) {
	var n int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, topic, key, payload::text FROM outbox
			WHERE published_at IS NULL ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
		if err != nil {
			return fmt.Errorf("read outbox: %w", err)
		}
		msgs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (delivery.OutboxMessage, error) {
			var (
				m       delivery.OutboxMessage
				payload string
			)
			err := row.Scan(&m.ID, &m.Topic, &m.Key, &payload)
			m.Payload = []byte(payload)
			return m, err
		})
		if err != nil {
			return fmt.Errorf("read outbox: %w", err)
		}
		if len(msgs) == 0 {
			return nil
		}
		if err := publish(ctx, msgs); err != nil {
			return fmt.Errorf("publish outbox: %w", err)
		}
		ids := make([]int64, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at = clock_timestamp() WHERE id = ANY($1)`, ids); err != nil {
			return fmt.Errorf("mark outbox published: %w", err)
		}
		n = len(msgs)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// TokenBucket implements delivery.TokenBucket with one row per channel in
// rate_limit_buckets. Rate is tokens per second and Burst the bucket's
// capacity: any one-second window holds at most Rate + Burst sends per
// channel, across every worker.
type TokenBucket struct {
	pool  *pgxpool.Pool
	rate  float64
	burst float64
}

var _ delivery.TokenBucket = (*TokenBucket)(nil)

// NewTokenBucket returns a bucket refilling at rate per second up to burst.
func NewTokenBucket(pool *pgxpool.Pool, rate, burst int) *TokenBucket {
	return &TokenBucket{pool: pool, rate: float64(rate), burst: float64(burst)}
}

// takeQuery refills the bucket for the time elapsed since its last update
// and takes one token if one is available, in one atomic statement. The
// row lock serializes concurrent takers, and the sub-select evaluates
// clock_timestamp() once, after the lock, so the stored time and the
// refill agree. RETURNING recomputes the pre-take level from OLD and NEW
// (Postgres 18) to report whether the token was granted.
const takeQuery = `UPDATE rate_limit_buckets b SET (tokens, updated_at) = (
	SELECT CASE WHEN a.avail >= 1 THEN a.avail - 1 ELSE a.avail END, a.ts
	FROM (SELECT least($2::float8, b.tokens + greatest(0, extract(epoch FROM t.ts - b.updated_at)::float8) * $3::float8) AS avail,
			t.ts
		FROM (SELECT clock_timestamp() AS ts) t) a)
WHERE channel = $1
RETURNING least($2::float8, old.tokens + greatest(0, extract(epoch FROM new.updated_at - old.updated_at)::float8) * $3::float8)`

// Take implements delivery.TokenBucket.
func (b *TokenBucket) Take(ctx context.Context, c content.Channel) (time.Duration, error) {
	var avail float64
	if err := b.pool.QueryRow(ctx, takeQuery, string(c), b.burst, b.rate).Scan(&avail); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("take rate-limit token: no bucket for channel %q", c)
		}
		return 0, fmt.Errorf("take rate-limit token: %w", err)
	}
	if avail >= 1 {
		return 0, nil
	}
	return time.Duration((1 - avail) / b.rate * float64(time.Second)), nil
}
