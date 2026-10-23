-- Delivery: one row per (campaign, recipient, channel), a transactional
-- outbox that carries delivery jobs to Kafka, and a token bucket per
-- channel shared by every worker. Postgres is the source of truth for
-- delivery and rate-limit state; Kafka only transports work.

-- Sending states follow approval: approved → sending → completed. Every
-- approved-or-later campaign keeps its approver.
ALTER TABLE campaigns DROP CONSTRAINT campaigns_status_check;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_status_check
    CHECK (status IN ('draft', 'audience_ready', 'drafts_ready', 'approved', 'sending', 'completed'));
ALTER TABLE campaigns DROP CONSTRAINT campaigns_check1;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_approval_check
    CHECK ((status IN ('approved', 'sending', 'completed')) = (approved_by IS NOT NULL AND approved_at IS NOT NULL));

ALTER TABLE campaign_events DROP CONSTRAINT campaign_events_action_check;
ALTER TABLE campaign_events ADD CONSTRAINT campaign_events_action_check
    CHECK (action IN ('created', 'audience_proposed', 'drafts_generated', 'edited', 'approved',
                      'sending_started', 'completed'));

-- id doubles as the provider idempotency key. attempts counts claims and
-- is the fencing token: an outcome is recorded only by the claim that
-- holds the current attempt.
CREATE TABLE deliveries (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    campaign_id         uuid        NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    user_id             bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    channel             text        NOT NULL CHECK (channel IN ('sms', 'email')),
    status              text        NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'processing', 'sent', 'failed', 'retrying')),
    attempts            integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts        integer     NOT NULL CHECK (max_attempts BETWEEN 1 AND 20),
    next_attempt_at     timestamptz NOT NULL DEFAULT now(),
    locked_until        timestamptz,
    last_error_code     text        CHECK (char_length(last_error_code) BETWEEN 1 AND 64),
    provider_message_id text        CHECK (char_length(provider_message_id) BETWEEN 1 AND 128),
    sent_at             timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    -- The same message never goes to the same person twice on a channel.
    UNIQUE (campaign_id, user_id, channel),
    CHECK (attempts <= max_attempts),
    CHECK ((status = 'processing') = (locked_until IS NOT NULL)),
    CHECK ((status = 'sent') = (sent_at IS NOT NULL AND provider_message_id IS NOT NULL))
);

-- Retry scheduler: due retries. Lease reaper: expired claims.
CREATE INDEX deliveries_due_retry_idx ON deliveries (next_attempt_at) WHERE status = 'retrying';
CREATE INDEX deliveries_expired_lease_idx ON deliveries (locked_until) WHERE status = 'processing';
-- Completion checks and progress counts per campaign.
CREATE INDEX deliveries_campaign_status_idx ON deliveries (campaign_id, status);

-- Rows are written in the same transaction as the state they announce and
-- published by the worker's relay (at least once).
CREATE TABLE outbox (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic        text        NOT NULL CHECK (char_length(topic) BETWEEN 1 AND 255),
    key          text        NOT NULL,
    payload      jsonb       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);

CREATE INDEX outbox_unpublished_idx ON outbox (id) WHERE published_at IS NULL;
CREATE INDEX outbox_published_at_idx ON outbox (published_at) WHERE published_at IS NOT NULL;

-- One token bucket per channel. Rate and burst are passed by the workers
-- on every acquisition; the bucket only stores its level and the database
-- time it was last refilled, so no worker clock is involved.
CREATE TABLE rate_limit_buckets (
    channel    text             PRIMARY KEY CHECK (channel IN ('sms', 'email')),
    tokens     double precision NOT NULL,
    updated_at timestamptz      NOT NULL DEFAULT clock_timestamp()
);

INSERT INTO rate_limit_buckets (channel, tokens) VALUES ('sms', 0), ('email', 0);
