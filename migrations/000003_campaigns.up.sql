-- Campaigns and their audit log. The status CHECK mirrors the domain
-- state machine (internal/campaign); later milestones extend it with
-- sending states. Approval columns are set together with the approved
-- status, enforced below, so a half-approved row cannot exist.
CREATE TABLE campaigns (
    id                    uuid        PRIMARY KEY DEFAULT uuidv7(),
    brief                 text        NOT NULL CHECK (char_length(brief) BETWEEN 1 AND 1000),
    status                text        NOT NULL
        CHECK (status IN ('draft', 'audience_ready', 'drafts_ready', 'approved')),
    segment_rule          jsonb,
    segment_explanation   text        NOT NULL DEFAULT '',
    unsupported_criteria  jsonb       NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(unsupported_criteria) = 'array'),
    audience_total        bigint      NOT NULL DEFAULT 0 CHECK (audience_total >= 0),
    audience_sms          bigint      NOT NULL DEFAULT 0 CHECK (audience_sms >= 0),
    audience_email        bigint      NOT NULL DEFAULT 0 CHECK (audience_email >= 0),
    channels              text[]      NOT NULL DEFAULT '{}' CHECK (channels <@ ARRAY['sms', 'email']::text[]),
    sms_body              text        NOT NULL DEFAULT '',
    email_subject         text        NOT NULL DEFAULT '',
    email_body            text        NOT NULL DEFAULT '',
    version               integer     NOT NULL DEFAULT 1 CHECK (version >= 1),
    content_hash          text        NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    -- Draft generation lease: at most one generation per campaign, held
    -- across API instances and expiring if its process dies.
    generation_token      text,
    generation_expires_at timestamptz,
    approved_by           text        CHECK (char_length(approved_by) BETWEEN 1 AND 100),
    approved_at           timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),

    CHECK (status = 'draft' OR segment_rule IS NOT NULL),
    CHECK ((status = 'approved') = (approved_by IS NOT NULL AND approved_at IS NOT NULL)),
    CHECK ((generation_token IS NULL) = (generation_expires_at IS NULL))
);

CREATE INDEX campaigns_created_at_idx ON campaigns (created_at DESC);

-- Append-only audit of status transitions and edits: who, when, from → to.
-- It never stores message content or recipient data.
CREATE TABLE campaign_events (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    campaign_id uuid        NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    action      text        NOT NULL
        CHECK (action IN ('created', 'audience_proposed', 'drafts_generated', 'edited', 'approved')),
    actor       text        NOT NULL CHECK (char_length(actor) BETWEEN 1 AND 100),
    from_status text,
    to_status   text        NOT NULL,
    version     integer     NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX campaign_events_campaign_idx ON campaign_events (campaign_id, id);

-- Append-only: audit rows cannot be updated. Rows go away only with their
-- campaign (ON DELETE CASCADE); nothing in the application deletes either.
CREATE FUNCTION campaign_events_append_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'campaign_events is append-only';
END;
$$;

CREATE TRIGGER campaign_events_no_update
    BEFORE UPDATE ON campaign_events
    FOR EACH ROW EXECUTE FUNCTION campaign_events_append_only();
