//go:build integration

package postgres_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/monitoring"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/postgres"
)

// setDelivery forces one delivery into a status, satisfying the table's
// CHECK constraints. sentAgo places sent_at in the past.
func (e campaignEnv) setDelivery(t *testing.T, id campaign.ID, userID int64, channel, status, code string, sentAgo time.Duration) {
	t.Helper()
	tag, err := e.pool.Exec(t.Context(), `UPDATE deliveries SET status = $4,
			last_error_code = NULLIF($5, ''),
			locked_until = CASE WHEN $4 = 'processing' THEN now() + interval '1 minute' END,
			sent_at = CASE WHEN $4 = 'sent' THEN clock_timestamp() - $6 * interval '1 microsecond' END,
			provider_message_id = CASE WHEN $4 = 'sent' THEN 'msg-' || id END
		WHERE campaign_id = $1 AND user_id = $2 AND channel = $3`,
		string(id), userID, channel, status, code, sentAgo.Microseconds())
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("set delivery user %d %s to %s: rows=%d err=%v", userID, channel, status, tag.RowsAffected(), err)
	}
}

// addDelivery inserts a queued delivery outside approval, so a fixture
// can hold more rows per channel than the example audience has.
func (e campaignEnv) addDelivery(t *testing.T, id campaign.ID, userID int64, channel string) {
	t.Helper()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO deliveries (campaign_id, user_id, channel, max_attempts)
		VALUES ($1, $2, $3, 5)`, string(id), userID, channel); err != nil {
		t.Fatalf("add delivery: %v", err)
	}
}

func TestProgressRepository_CountsEveryStatusAndChannel(t *testing.T) {
	e := newCampaignEnv(t)
	c, _ := e.approved(t) // sms: users 1, 2, 6, 10; email: 1, 4, 6, 10
	for _, u := range []int64{3, 5, 7, 8, 9} {
		e.addDelivery(t, c.ID, u, "sms")
	}
	for _, u := range []int64{2, 3} {
		e.addDelivery(t, c.ID, u, "email")
	}
	const old = time.Hour
	for _, d := range []struct {
		user         int64
		channel      string
		status, code string
		sentAgo      time.Duration
	}{
		{1, "sms", "queued", "", 0},
		{10, "sms", "queued", "", 0},
		{2, "sms", "processing", "", 0},
		{3, "sms", "sent", "", 0},
		{5, "sms", "sent", "", old},
		{6, "sms", "failed", "consent_revoked", 0},
		{7, "sms", "failed", "provider_rejected", 0},
		{8, "sms", "retrying", "provider_unavailable", 0},
		{9, "sms", "retrying", "provider_rate_limited", 0},
		{10, "email", "queued", "", 0},
		{2, "email", "processing", "", 0},
		{1, "email", "sent", "", 0},
		{4, "email", "sent", "", 0},
		{6, "email", "failed", "provider_rejected", 0},
		{3, "email", "retrying", "provider_timeout", 0},
	} {
		e.setDelivery(t, c.ID, d.user, d.channel, d.status, d.code, d.sentAgo)
	}
	// Another campaign's deliveries never count.
	e.approved(t)

	repo := postgres.NewProgressRepository(e.pool)
	snap, err := repo.Progress(t.Context(), c.ID, time.Minute)
	if err != nil {
		t.Fatalf("Progress() error = %v", err)
	}

	if snap.CampaignID != c.ID || snap.CampaignStatus != campaign.StatusApproved || snap.Window != time.Minute {
		t.Errorf("snapshot header = %s %s %s", snap.CampaignID, snap.CampaignStatus, snap.Window)
	}
	wantChannels := []monitoring.ChannelProgress{
		{Channel: content.ChannelSMS, Counts: monitoring.StatusCounts{Queued: 2, Processing: 1, Sent: 2, Failed: 2, Retrying: 2}, RecentSent: 1},
		{Channel: content.ChannelEmail, Counts: monitoring.StatusCounts{Queued: 1, Processing: 1, Sent: 2, Failed: 1, Retrying: 1}, RecentSent: 2},
	}
	if !slices.Equal(snap.Channels, wantChannels) {
		t.Errorf("channels = %+v\nwant       %+v", snap.Channels, wantChannels)
	}
	wantFailures := []monitoring.FailureCount{{Code: "provider_rejected", Count: 2}, {Code: "consent_revoked", Count: 1}}
	if !slices.Equal(snap.Failures, wantFailures) {
		t.Errorf("failures = %+v, want %+v", snap.Failures, wantFailures)
	}
	if snap.GeneratedAt.IsZero() {
		t.Error("generated_at not set")
	}

	// The final numbers equal a direct count over the same rows.
	if got, want := snap.Totals().Total(), int64(count(t, e.pool,
		`SELECT count(*) FROM deliveries WHERE campaign_id = $1`, string(c.ID))); got != want {
		t.Errorf("total = %d, direct count = %d", got, want)
	}
}

func TestProgressRepository_CampaignWithoutDeliveries(t *testing.T) {
	e := newCampaignEnv(t)
	c := e.ready(t)

	snap, err := postgres.NewProgressRepository(e.pool).Progress(t.Context(), c.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	want := []monitoring.ChannelProgress{{Channel: content.ChannelSMS}, {Channel: content.ChannelEmail}}
	if snap.CampaignStatus != campaign.StatusDraftsReady || !slices.Equal(snap.Channels, want) || len(snap.Failures) != 0 {
		t.Errorf("snapshot = %+v, want drafts_ready with zero counts on both selected channels", snap)
	}
}

func TestProgressRepository_UnknownCampaign(t *testing.T) {
	e := newCampaignEnv(t)
	_, err := postgres.NewProgressRepository(e.pool).Progress(t.Context(), "0196f0a2-0000-7000-8000-000000000000", time.Minute)
	if !errors.Is(err, campaign.ErrNotFound) {
		t.Errorf("Progress() error = %v, want campaign.ErrNotFound", err)
	}
}
