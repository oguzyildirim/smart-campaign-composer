//go:build integration

package postgres_test

import (
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/llm/mockllm"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/postgres"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
	"github.com/oguzyildirim/smart-campaign-composer/internal/testutil/pgtest"
)

// settableClock is the service clock of the scheduling tests.
type settableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *settableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *settableClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type scheduleEnv struct {
	campaignEnv
	clock *settableClock
}

var testSchedule = campaign.ScheduleConfig{MinLead: time.Minute, MaxHorizon: 30 * 24 * time.Hour, ReleaseGrace: 15 * time.Minute}

// newScheduleEnv is newCampaignEnv with a clock the test moves. It starts
// at refNow, which is in the database clock's past.
func newScheduleEnv(t *testing.T) scheduleEnv {
	t.Helper()
	dsn := pgtest.Start(t)
	pgtest.Migrate(t, dsn)
	pool, err := postgres.NewPool(t.Context(), dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	loadFixture(t, pool)

	clock := &settableClock{now: refNow}
	logger := slog.New(slog.DiscardHandler)
	audience := postgres.NewAudienceRepository(pool)
	gen := campaign.NewGenerator(mockllm.New(), segment.NewService(audience, clock.Now), campaign.GeneratorConfig{
		SegmentTimeout: 5 * time.Second, DraftsTimeout: 5 * time.Second, MaxAttempts: 2,
	}, logger)
	store := postgres.NewCampaignStore(pool)
	svc := campaign.NewService(gen, store, audience, campaign.ServiceConfig{
		GenerationTimeout: 10 * time.Second, PreviewSamples: 3, DeliveryMaxAttempts: 5, Schedule: testSchedule,
	}, clock.Now, logger)
	return scheduleEnv{campaignEnv: campaignEnv{pool: pool, store: store, svc: svc}, clock: clock}
}

func (e scheduleEnv) schedule(t *testing.T, at time.Time) campaign.Campaign {
	t.Helper()
	c := e.ready(t)
	a := approvalOf(c)
	a.ScheduledAt = at
	out, err := e.svc.Approve(t.Context(), c.ID, a)
	if err != nil {
		t.Fatalf("Approve(scheduled) error = %v", err)
	}
	return out
}

// deliveryUsers returns the user ids (fixture positions, 1-based) with a
// delivery on channel.
func deliveryUsers(t *testing.T, e scheduleEnv, id campaign.ID, channel string) []int64 {
	t.Helper()
	rows, err := e.pool.Query(t.Context(), `SELECT u.id - (SELECT min(id) FROM users) + 1 FROM deliveries d
		JOIN users u ON u.id = d.user_id WHERE d.campaign_id = $1 AND d.channel = $2 ORDER BY 1`, string(id), channel)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func countRows(t *testing.T, e scheduleEnv, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSchedule_StoreRoundTrip(t *testing.T) {
	e := newScheduleEnv(t)
	at := refNow.Add(2 * time.Hour)
	c := e.schedule(t, at)
	got, err := e.svc.Get(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != campaign.StatusScheduled || !got.ScheduledAt.Equal(at) || got.ApprovedBy != "Deniz Operator" {
		t.Errorf("stored = %+v", got)
	}
	if n := countRows(t, e, `SELECT count(*) FROM deliveries WHERE campaign_id = $1`, string(c.ID)); n != 0 {
		t.Errorf("scheduled approval created %d deliveries", n)
	}
	snap, err := postgres.NewProgressRepository(e.pool).Progress(t.Context(), c.ID, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if snap.CampaignStatus != campaign.StatusScheduled || !snap.ScheduledAt.Equal(at) || snap.Totals().Total() != 0 || len(snap.Channels) == 0 {
		t.Errorf("progress of a scheduled campaign = %+v", snap)
	}
	evs := events(t, e.pool, c.ID)
	if last := evs[len(evs)-1]; last != (eventRow{"scheduled", "Deniz Operator", "drafts_ready", "scheduled", got.Version}) {
		t.Errorf("event = %+v", last)
	}

	// A failed release stores its reasons and a cancel clears them.
	if _, err := e.pool.Exec(t.Context(), `UPDATE channel_consents SET granted = false`); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(at)
	if res, err := e.svc.Release(t.Context(), c.ID); err != nil || res.Outcome != campaign.ReleaseFailed {
		t.Fatalf("Release() = %+v, %v", res, err)
	}
	failed, _ := e.svc.Get(t.Context(), c.ID)
	if failed.Status != campaign.StatusReleaseFailed || failed.ReleaseFailure == nil ||
		failed.ReleaseFailure.Code != campaign.CodeNoRecipients || !failed.ScheduledAt.Equal(at) {
		t.Fatalf("release_failed = %+v (failure %+v)", failed, failed.ReleaseFailure)
	}
	canceled, err := e.svc.Cancel(t.Context(), c.ID, campaign.Cancel{Version: failed.Version, ContentHash: failed.ContentHash, Actor: "Ece"})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if canceled.Status != campaign.StatusDraftsReady || canceled.ReleaseFailure != nil || !canceled.ScheduledAt.IsZero() ||
		canceled.ApprovedBy != "" {
		t.Errorf("canceled = %+v", canceled)
	}
	actions := []string{}
	for _, ev := range events(t, e.pool, c.ID) {
		actions = append(actions, ev.action)
	}
	if !slices.Equal(actions[len(actions)-3:], []string{"scheduled", "release_failed", "schedule_canceled"}) {
		t.Errorf("actions = %v", actions)
	}
}

func TestSchedule_Constraints(t *testing.T) {
	e := newScheduleEnv(t)
	c := e.schedule(t, refNow.Add(time.Hour))
	ctx := t.Context()
	bad := []struct {
		name, sql string
	}{
		{"scheduled without a time", `UPDATE campaigns SET scheduled_at = NULL WHERE id = $1`},
		{"scheduled without approval", `UPDATE campaigns SET approved_by = NULL, approved_at = NULL WHERE id = $1`},
		{"drafts_ready with a time", `UPDATE campaigns SET status = 'drafts_ready', approved_by = NULL, approved_at = NULL WHERE id = $1`},
		{"release_failed without reasons", `UPDATE campaigns SET status = 'release_failed' WHERE id = $1`},
		{"reasons outside release_failed", `UPDATE campaigns SET release_failure = '{"code":"x"}' WHERE id = $1`},
		{"release reasons not an object", `UPDATE campaigns SET status = 'release_failed', release_failure = '[]' WHERE id = $1`},
		{"unknown status", `UPDATE campaigns SET status = 'paused' WHERE id = $1`},
		{"unknown event action", `INSERT INTO campaign_events (campaign_id, action, actor, to_status, version)
			VALUES ($1, 'paused', 'system', 'scheduled', 1)`},
	}
	for _, tt := range bad {
		if _, err := e.pool.Exec(ctx, tt.sql, string(c.ID)); err == nil {
			t.Errorf("%s: accepted", tt.name)
		}
	}
	good := []string{
		`UPDATE campaigns SET status = 'expired' WHERE id = $1`,
		`UPDATE campaigns SET status = 'release_failed', release_failure = '{"code":"no_recipients"}' WHERE id = $1`,
		`UPDATE campaigns SET status = 'approved', release_failure = NULL WHERE id = $1`, // released keeps its time
	}
	for _, sql := range good {
		if _, err := e.pool.Exec(ctx, sql, string(c.ID)); err != nil {
			t.Errorf("%s: %v", sql, err)
		}
	}
}

func TestDueScheduled_UsesDatabaseClockAndSkipsLocked(t *testing.T) {
	e := newScheduleEnv(t)
	ctx := t.Context()
	past := e.schedule(t, refNow.Add(time.Hour))
	older := e.schedule(t, refNow.Add(time.Hour))
	future := e.schedule(t, refNow.Add(time.Hour))
	e.ready(t) // drafts_ready, never due
	// Place the scheduled times relative to the database clock.
	for id, offset := range map[campaign.ID]string{past.ID: "-1 minute", older.ID: "-1 hour", future.ID: "+1 hour"} {
		if _, err := e.pool.Exec(ctx, `UPDATE campaigns SET scheduled_at = clock_timestamp() + $2::interval WHERE id = $1`,
			string(id), offset); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := e.store.DueScheduled(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []campaign.ID{older.ID, past.ID}) {
		t.Errorf("due = %v, want [%s %s] (oldest first)", ids, older.ID, past.ID)
	}
	if ids, _ := e.store.DueScheduled(ctx, 1); !slices.Equal(ids, []campaign.ID{older.ID}) {
		t.Errorf("due(limit 1) = %v", ids)
	}

	// A campaign another transaction holds (a release or cancel in
	// progress) is skipped rather than waited for.
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM campaigns WHERE id = $1 FOR UPDATE`, string(older.ID)); err != nil {
		t.Fatal(err)
	}
	if ids, _ := e.store.DueScheduled(ctx, 10); !slices.Equal(ids, []campaign.ID{past.ID}) {
		t.Errorf("due with %s locked = %v", older.ID, ids)
	}
}

func TestRelease_ConcurrentCallersCreateOneSet(t *testing.T) {
	e := newScheduleEnv(t)
	at := refNow.Add(2 * 24 * time.Hour)
	c := e.schedule(t, at)
	e.clock.Set(at.Add(10 * time.Second))

	var wg sync.WaitGroup
	results := make(chan campaign.ReleaseOutcome, 8)
	for range 8 {
		wg.Go(func() {
			res, err := e.svc.Release(t.Context(), c.ID)
			if err != nil {
				t.Errorf("Release() error = %v", err)
			}
			results <- res.Outcome
		})
	}
	wg.Wait()
	close(results)
	released := 0
	for o := range results {
		if o == campaign.ReleaseReleased {
			released++
		}
	}
	if released != 1 {
		t.Errorf("releases = %d, want exactly 1", released)
	}
	if n := countRows(t, e, `SELECT count(*) FROM campaign_events WHERE campaign_id = $1 AND action = 'released'`, string(c.ID)); n != 1 {
		t.Errorf("release events = %d, want 1", n)
	}
	deliveries := countRows(t, e, `SELECT count(*) FROM deliveries WHERE campaign_id = $1`, string(c.ID))
	outbox := countRows(t, e, `SELECT count(*) FROM outbox WHERE payload->>'delivery_id' IN
		(SELECT id::text FROM deliveries WHERE campaign_id = $1)`, string(c.ID))
	if deliveries == 0 || outbox != deliveries {
		t.Errorf("deliveries = %d, outbox = %d; want one queued message per delivery", deliveries, outbox)
	}
}

func TestRelease_RaceWithCancelHasOneWinner(t *testing.T) {
	for range 3 {
		e := newScheduleEnv(t)
		at := refNow.Add(time.Hour)
		c := e.schedule(t, at)
		e.clock.Set(at)

		var (
			wg        sync.WaitGroup
			res       campaign.ReleaseResult
			relErr    error
			cancelErr error
		)
		wg.Go(func() { res, relErr = e.svc.Release(t.Context(), c.ID) })
		wg.Go(func() {
			_, cancelErr = e.svc.Cancel(t.Context(), c.ID, campaign.Cancel{Version: c.Version, ContentHash: c.ContentHash, Actor: "Ece"})
		})
		wg.Wait()
		if relErr != nil {
			t.Fatalf("Release() error = %v", relErr)
		}
		got, _ := e.svc.Get(t.Context(), c.ID)
		n := countRows(t, e, `SELECT count(*) FROM deliveries WHERE campaign_id = $1`, string(c.ID))
		switch res.Outcome {
		case campaign.ReleaseReleased:
			var cerr *campaign.Error
			if !errors.As(cancelErr, &cerr) || cerr.Code != campaign.CodeInvalidState || got.Status != campaign.StatusApproved || n == 0 {
				t.Errorf("release won: cancel error %v, status %s, deliveries %d", cancelErr, got.Status, n)
			}
		case campaign.ReleaseNone:
			if cancelErr != nil || got.Status != campaign.StatusDraftsReady || n != 0 {
				t.Errorf("cancel won: cancel error %v, status %s, deliveries %d", cancelErr, got.Status, n)
			}
		default:
			t.Errorf("outcome = %s", res.Outcome)
		}
	}
}

// TestRelease_AudienceAtReleaseTime schedules the example campaign two days
// after refNow. Between scheduling and release user 1 revokes SMS consent.
// At release, the 30-day windows end at the scheduled time: user 9 (cart
// one day after refNow) newly matches, users 2 and 10 (carts 29 and 30
// days before refNow) no longer do.
func TestRelease_AudienceAtReleaseTime(t *testing.T) {
	e := newScheduleEnv(t)
	at := refNow.Add(2 * 24 * time.Hour)
	c := e.schedule(t, at)
	if _, err := e.pool.Exec(t.Context(), `UPDATE channel_consents SET granted = false
		WHERE channel = 'sms' AND user_id = (SELECT min(id) FROM users)`); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(at.Add(30 * time.Second))
	if res, err := e.svc.Release(t.Context(), c.ID); err != nil || res.Outcome != campaign.ReleaseReleased {
		t.Fatalf("Release() = %+v, %v", res, err)
	}
	if got := deliveryUsers(t, e, c.ID, "sms"); !slices.Equal(got, []int64{6, 9}) {
		t.Errorf("sms recipients = %v, want [6 9]", got)
	}
	if got := deliveryUsers(t, e, c.ID, "email"); !slices.Equal(got, []int64{1, 4, 6, 9}) {
		t.Errorf("email recipients = %v, want [1 4 6 9]", got)
	}
	got, _ := e.svc.Get(t.Context(), c.ID)
	if got.Status != campaign.StatusApproved || !got.ScheduledAt.Equal(at) {
		t.Errorf("released = %+v", got)
	}
}

func TestRelease_ExpiredCreatesNothing(t *testing.T) {
	e := newScheduleEnv(t)
	at := refNow.Add(time.Hour)
	c := e.schedule(t, at)
	e.clock.Set(at.Add(testSchedule.ReleaseGrace + time.Second))
	if res, err := e.svc.Release(t.Context(), c.ID); err != nil || res.Outcome != campaign.ReleaseExpired {
		t.Fatalf("Release() = %+v, %v", res, err)
	}
	if n := countRows(t, e, `SELECT count(*) FROM deliveries WHERE campaign_id = $1`, string(c.ID)); n != 0 {
		t.Errorf("expired campaign has %d deliveries", n)
	}
	evs := events(t, e.pool, c.ID)
	if last := evs[len(evs)-1]; last.action != "expired" || last.actor != "system" || last.to != "expired" {
		t.Errorf("event = %+v", last)
	}
}
