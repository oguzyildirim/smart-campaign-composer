-- Scheduled delivery: a campaign approved for a later time is 'scheduled'
-- (approval recorded, no deliveries yet) until a worker releases it
-- (→ 'approved', deliveries created), its release checks fail
-- ('release_failed'), or it is not released within the grace period
-- ('expired'). Each of these three carries the approval, like the
-- approved-or-later states, until a cancel returns it to drafts_ready.
ALTER TABLE campaigns DROP CONSTRAINT campaigns_status_check;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_status_check
    CHECK (status IN ('draft', 'audience_ready', 'drafts_ready', 'scheduled', 'release_failed', 'expired',
                      'approved', 'sending', 'completed'));
ALTER TABLE campaigns DROP CONSTRAINT campaigns_approval_check;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_approval_check
    CHECK ((status IN ('scheduled', 'release_failed', 'expired', 'approved', 'sending', 'completed'))
           = (approved_by IS NOT NULL AND approved_at IS NOT NULL));

-- scheduled_at is required while scheduled and kept afterwards for audit
-- (a released campaign keeps it; one approved to send now has none). A
-- cancel clears it together with the approval.
ALTER TABLE campaigns ADD COLUMN scheduled_at timestamptz;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_scheduled_at_check
    CHECK ((status NOT IN ('scheduled', 'release_failed', 'expired') OR scheduled_at IS NOT NULL)
           AND (status NOT IN ('draft', 'audience_ready', 'drafts_ready') OR scheduled_at IS NULL));

-- Why a release failed: error code, message, and field errors. Never
-- message content or recipient data.
ALTER TABLE campaigns ADD COLUMN release_failure jsonb;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_release_failure_check
    CHECK ((status = 'release_failed') = (release_failure IS NOT NULL)
           AND (release_failure IS NULL OR jsonb_typeof(release_failure) = 'object'));

-- The release loop's due scan.
CREATE INDEX campaigns_scheduled_due_idx ON campaigns (scheduled_at) WHERE status = 'scheduled';

ALTER TABLE campaign_events DROP CONSTRAINT campaign_events_action_check;
ALTER TABLE campaign_events ADD CONSTRAINT campaign_events_action_check
    CHECK (action IN ('created', 'audience_proposed', 'drafts_generated', 'edited', 'approved',
                      'sending_started', 'completed',
                      'scheduled', 'rescheduled', 'schedule_canceled', 'released', 'release_failed', 'expired'));
