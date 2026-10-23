DROP TABLE IF EXISTS rate_limit_buckets;
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS deliveries;

-- Campaigns past approval cannot be represented by the previous schema.
DELETE FROM campaigns WHERE status IN ('sending', 'completed');

ALTER TABLE campaign_events DROP CONSTRAINT campaign_events_action_check;
DELETE FROM campaign_events WHERE action IN ('sending_started', 'completed');
ALTER TABLE campaign_events ADD CONSTRAINT campaign_events_action_check
    CHECK (action IN ('created', 'audience_proposed', 'drafts_generated', 'edited', 'approved'));

ALTER TABLE campaigns DROP CONSTRAINT campaigns_approval_check;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_check1
    CHECK ((status = 'approved') = (approved_by IS NOT NULL AND approved_at IS NOT NULL));
ALTER TABLE campaigns DROP CONSTRAINT campaigns_status_check;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_status_check
    CHECK (status IN ('draft', 'audience_ready', 'drafts_ready', 'approved'));
