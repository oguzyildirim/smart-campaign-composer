-- Campaigns approved for later but not sent cannot be represented by the
-- previous schema; they go back to drafts_ready without their approval,
-- as a cancel would. Released campaigns keep their status.
UPDATE campaigns SET status = 'drafts_ready', approved_by = NULL, approved_at = NULL, release_failure = NULL,
    scheduled_at = NULL, version = version + 1, updated_at = now()
WHERE status IN ('scheduled', 'release_failed', 'expired');

ALTER TABLE campaign_events DROP CONSTRAINT campaign_events_action_check;
DELETE FROM campaign_events
WHERE action IN ('scheduled', 'rescheduled', 'schedule_canceled', 'released', 'release_failed', 'expired');
ALTER TABLE campaign_events ADD CONSTRAINT campaign_events_action_check
    CHECK (action IN ('created', 'audience_proposed', 'drafts_generated', 'edited', 'approved',
                      'sending_started', 'completed'));

DROP INDEX IF EXISTS campaigns_scheduled_due_idx;
ALTER TABLE campaigns DROP CONSTRAINT campaigns_release_failure_check;
ALTER TABLE campaigns DROP COLUMN release_failure;
ALTER TABLE campaigns DROP CONSTRAINT campaigns_scheduled_at_check;
ALTER TABLE campaigns DROP COLUMN scheduled_at;

ALTER TABLE campaigns DROP CONSTRAINT campaigns_approval_check;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_approval_check
    CHECK ((status IN ('approved', 'sending', 'completed')) = (approved_by IS NOT NULL AND approved_at IS NOT NULL));
ALTER TABLE campaigns DROP CONSTRAINT campaigns_status_check;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_status_check
    CHECK (status IN ('draft', 'audience_ready', 'drafts_ready', 'approved', 'sending', 'completed'));
