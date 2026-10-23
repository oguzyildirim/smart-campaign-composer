//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/delivery"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/postgres"
)

// Users matching the example rule in the audience fixture, by channel
// consent (see audience_integration_test.go).
var (
	wantSMSUsers   = []int64{1, 2, 6, 10}
	wantEmailUsers = []int64{1, 4, 6, 10}
)

type deliveryRow struct {
	id, channel, status, code string
	userID                    int64
	attempts, maxAttempts     int
}

func deliveriesOf(t *testing.T, pool *pgxpool.Pool, id campaign.ID) []deliveryRow {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT id::text, channel, status, coalesce(last_error_code, ''), user_id,
		attempts, max_attempts FROM deliveries WHERE campaign_id = $1 ORDER BY channel, user_id`, string(id))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []deliveryRow
	for rows.Next() {
		var r deliveryRow
		if err := rows.Scan(&r.id, &r.channel, &r.status, &r.code, &r.userID, &r.attempts, &r.maxAttempts); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func usersOn(rows []deliveryRow, channel string) []int64 {
	var out []int64
	for _, r := range rows {
		if r.channel == channel {
			out = append(out, r.userID)
		}
	}
	return out
}

func count(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// approved returns the example campaign approved, with its deliveries.
func (e campaignEnv) approved(t *testing.T) (campaign.Campaign, []deliveryRow) {
	t.Helper()
	c := e.ready(t)
	c, err := e.svc.Approve(t.Context(), c.ID, approvalOf(c))
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	return c, deliveriesOf(t, e.pool, c.ID)
}

func campaignStatus(t *testing.T, pool *pgxpool.Pool, id campaign.ID) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM campaigns WHERE id = $1`, string(id)).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestApprove_MaterializesConsentedDeliveries(t *testing.T) {
	e := newCampaignEnv(t)
	c, rows := e.approved(t)

	if got := usersOn(rows, "sms"); !slices.Equal(got, wantSMSUsers) {
		t.Errorf("sms deliveries for users %v, want %v", got, wantSMSUsers)
	}
	if got := usersOn(rows, "email"); !slices.Equal(got, wantEmailUsers) {
		t.Errorf("email deliveries for users %v, want %v", got, wantEmailUsers)
	}
	for _, r := range rows {
		if r.status != "queued" || r.attempts != 0 || r.maxAttempts != 5 {
			t.Errorf("delivery %+v, want queued with 0/5 attempts", r)
		}
	}

	// One outbox message per delivery, on the channel's topic.
	outRows, err := e.pool.Query(t.Context(), `SELECT topic, key, payload::text FROM outbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer outRows.Close()
	byID := map[string]string{}
	for _, r := range rows {
		byID[r.id] = r.channel
	}
	n := 0
	for outRows.Next() {
		var topic, key, payload string
		if err := outRows.Scan(&topic, &key, &payload); err != nil {
			t.Fatal(err)
		}
		id, err := delivery.DecodeJob([]byte(payload))
		if err != nil || string(id) != key || topic != "deliveries."+byID[key] {
			t.Errorf("outbox row topic=%s key=%s payload=%s does not match a delivery", topic, key, payload)
		}
		n++
	}
	if n != len(rows) {
		t.Errorf("outbox rows = %d, want %d", n, len(rows))
	}

	// Re-approving the same content creates nothing.
	if _, err := e.svc.Approve(t.Context(), c.ID, approvalOf(c)); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
	if got := count(t, e.pool, `SELECT count(*) FROM deliveries`); got != len(rows) {
		t.Errorf("deliveries after re-approval = %d, want %d", got, len(rows))
	}
	if got := count(t, e.pool, `SELECT count(*) FROM outbox`); got != len(rows) {
		t.Errorf("outbox rows after re-approval = %d, want %d", got, len(rows))
	}
}

func TestApprove_ConcurrentApprovalsMaterializeOnce(t *testing.T) {
	e := newCampaignEnv(t)
	c := e.ready(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { _, _ = e.svc.Approve(t.Context(), c.ID, approvalOf(c)) })
	}
	wg.Wait()
	want := len(wantSMSUsers) + len(wantEmailUsers)
	if got := count(t, e.pool, `SELECT count(*) FROM deliveries`); got != want {
		t.Errorf("deliveries = %d, want %d", got, want)
	}
	if got := count(t, e.pool, `SELECT count(*) FROM outbox`); got != want {
		t.Errorf("outbox rows = %d, want %d", got, want)
	}
}

func TestApprove_SingleChannelOnlyMaterializesThatChannel(t *testing.T) {
	e := newCampaignEnv(t)
	c := e.ready(t)
	c, err := e.svc.Edit(t.Context(), c.ID, editOf(c, func(ed *campaign.Edit) { ed.Channels = []string{"email"} }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Approve(t.Context(), c.ID, approvalOf(c)); err != nil {
		t.Fatal(err)
	}
	rows := deliveriesOf(t, e.pool, c.ID)
	if len(usersOn(rows, "sms")) != 0 || !slices.Equal(usersOn(rows, "email"), wantEmailUsers) {
		t.Errorf("deliveries = %+v, want email only", rows)
	}
}

func TestDeliveryStore_ClaimIsExclusive(t *testing.T) {
	e := newCampaignEnv(t)
	c, rows := e.approved(t)
	store := postgres.NewDeliveryStore(e.pool)
	id := delivery.ID(rows[0].id)

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	for range 20 {
		wg.Go(func() {
			_, ok, err := store.Claim(t.Context(), id, time.Minute)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("claims won = %d, want 1", wins)
	}
	if s := campaignStatus(t, e.pool, c.ID); s != "sending" {
		t.Errorf("campaign status = %s, want sending after the first claim", s)
	}
	// A second delivery's claim does not repeat the transition.
	if _, ok, err := store.Claim(t.Context(), delivery.ID(rows[1].id), time.Minute); err != nil || !ok {
		t.Fatalf("claim second delivery: ok=%v err=%v", ok, err)
	}
	if n := count(t, e.pool, `SELECT count(*) FROM campaign_events WHERE campaign_id = $1 AND action = 'sending_started'`, string(c.ID)); n != 1 {
		t.Errorf("sending_started events = %d, want 1", n)
	}
}

func TestDeliveryStore_SentDeliveryIsNotClaimedAgain(t *testing.T) {
	e := newCampaignEnv(t)
	_, rows := e.approved(t)
	store := postgres.NewDeliveryStore(e.pool)
	id := delivery.ID(rows[0].id)

	job, ok, err := store.Claim(t.Context(), id, time.Minute)
	if err != nil || !ok || job.Attempt != 1 || job.MaxAttempts != 5 || job.UserID != rows[0].userID {
		t.Fatalf("Claim() = %+v, %v, %v", job, ok, err)
	}
	if recorded, err := store.Record(t.Context(), job, delivery.Outcome{Status: delivery.StatusSent, ProviderMessageID: "msg_1"}); err != nil || !recorded {
		t.Fatalf("Record() = %v, %v", recorded, err)
	}
	// A duplicate queue message after success is skipped.
	if _, ok, err := store.Claim(t.Context(), id, time.Minute); err != nil || ok {
		t.Fatalf("claim after sent: ok=%v err=%v, want not claimable", ok, err)
	}
	// So is a stale outcome for the same attempt.
	if recorded, _ := store.Record(t.Context(), job, delivery.Outcome{Status: delivery.StatusFailed, ErrorCode: "x"}); recorded {
		t.Error("a second outcome overwrote the sent delivery")
	}
}

func TestDeliveryStore_RetryIsNotClaimableUntilDue(t *testing.T) {
	e := newCampaignEnv(t)
	_, rows := e.approved(t)
	store := postgres.NewDeliveryStore(e.pool)
	id := delivery.ID(rows[0].id)

	job, _, _ := store.Claim(t.Context(), id, time.Minute)
	if _, err := store.Record(t.Context(), job, delivery.Outcome{
		Status: delivery.StatusRetrying, ErrorCode: delivery.CodeUnavailable, RetryIn: 300 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Claim(t.Context(), id, time.Minute); ok {
		t.Fatal("retrying delivery claimed before next_attempt_at")
	}
	if n, err := store.RequeueDue(t.Context(), 100); err != nil || n != 0 {
		t.Fatalf("RequeueDue() before due = %d, %v; want 0", n, err)
	}
	time.Sleep(350 * time.Millisecond)
	outboxBefore := count(t, e.pool, `SELECT count(*) FROM outbox`)
	if n, err := store.RequeueDue(t.Context(), 100); err != nil || n != 1 {
		t.Fatalf("RequeueDue() when due = %d, %v; want 1", n, err)
	}
	if got := count(t, e.pool, `SELECT count(*) FROM outbox`); got != outboxBefore+1 {
		t.Errorf("outbox rows = %d, want %d", got, outboxBefore+1)
	}
	job2, ok, err := store.Claim(t.Context(), id, time.Minute)
	if err != nil || !ok || job2.Attempt != 2 {
		t.Fatalf("claim after requeue = %+v, %v, %v; want attempt 2", job2, ok, err)
	}
}

func TestDeliveryStore_LeaseExpiryRecovery(t *testing.T) {
	e := newCampaignEnv(t)
	_, rows := e.approved(t)
	store := postgres.NewDeliveryStore(e.pool)
	id := delivery.ID(rows[0].id)

	stale, _, _ := store.Claim(t.Context(), id, 50*time.Millisecond)
	if n, _ := store.RecoverExpiredLeases(t.Context(), 100); n != 0 {
		t.Fatalf("recovered %d live leases", n)
	}
	time.Sleep(80 * time.Millisecond)
	if n, err := store.RecoverExpiredLeases(t.Context(), 100); err != nil || n != 1 {
		t.Fatalf("RecoverExpiredLeases() = %d, %v; want 1", n, err)
	}
	var status, code string
	var attempts int
	if err := e.pool.QueryRow(t.Context(), `SELECT status, last_error_code, attempts FROM deliveries WHERE id = $1`, string(id)).
		Scan(&status, &code, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "retrying" || code != delivery.CodeLeaseExpired || attempts != 1 {
		t.Errorf("after recovery: %s(%s) attempts %d, want retrying(lease_expired) with the attempt counted", status, code, attempts)
	}
	if _, err := store.RequeueDue(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	fresh, ok, err := store.Claim(t.Context(), id, time.Minute)
	if err != nil || !ok || fresh.Attempt != 2 {
		t.Fatalf("re-claim = %+v, %v, %v", fresh, ok, err)
	}
	// The original worker finishes late: its outcome is fenced off.
	if recorded, err := store.Record(t.Context(), stale, delivery.Outcome{Status: delivery.StatusSent, ProviderMessageID: "late"}); err != nil || recorded {
		t.Fatalf("stale Record() = %v, %v; want rejected", recorded, err)
	}
	if recorded, err := store.Record(t.Context(), fresh, delivery.Outcome{Status: delivery.StatusSent, ProviderMessageID: "msg"}); err != nil || !recorded {
		t.Fatalf("fresh Record() = %v, %v", recorded, err)
	}
}

func TestDeliveryStore_ExpiredLeaseOnLastAttemptFails(t *testing.T) {
	e := newCampaignEnv(t)
	_, rows := e.approved(t)
	store := postgres.NewDeliveryStore(e.pool)
	id := delivery.ID(rows[0].id)
	if _, err := e.pool.Exec(t.Context(), `UPDATE deliveries SET max_attempts = 1 WHERE id = $1`, string(id)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Claim(t.Context(), id, time.Millisecond); !ok {
		t.Fatal("claim failed")
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := store.RecoverExpiredLeases(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	var status, code string
	if err := e.pool.QueryRow(t.Context(), `SELECT status, last_error_code FROM deliveries WHERE id = $1`, string(id)).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || code != delivery.CodeRetriesExhausted {
		t.Errorf("status = %s(%s), want failed(retries_exhausted)", status, code)
	}
}

func TestDeliveryStore_ReleaseGivesTheAttemptBack(t *testing.T) {
	e := newCampaignEnv(t)
	_, rows := e.approved(t)
	store := postgres.NewDeliveryStore(e.pool)
	id := delivery.ID(rows[0].id)
	job, _, _ := store.Claim(t.Context(), id, time.Minute)
	if _, err := store.Record(t.Context(), job, delivery.Outcome{Status: delivery.StatusRetrying, ErrorCode: delivery.CodeInterrupted, Release: true}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, e.pool, `SELECT attempts FROM deliveries WHERE id = $1`, string(id)); n != 0 {
		t.Errorf("attempts = %d, want 0 after release", n)
	}
}

func TestDeliveryStore_Recipient(t *testing.T) {
	e := newCampaignEnv(t)
	c, rows := e.approved(t)
	store := postgres.NewDeliveryStore(e.pool)

	for _, r := range rows[:2] {
		job, _, _ := store.Claim(t.Context(), delivery.ID(r.id), time.Minute)
		got, err := store.Recipient(t.Context(), job)
		if err != nil {
			t.Fatal(err)
		}
		wantAddr := fmt.Sprintf("user%d@example.com", r.userID)
		if r.channel == "sms" {
			wantAddr = fmt.Sprintf("+9995%09d", r.userID)
		}
		if !got.Consented || got.Address != wantAddr || got.Values[content.VarFirstName] != "Test" ||
			got.Drafts != c.Drafts {
			t.Errorf("Recipient(%s) = %+v, want consented, %s, approved drafts", r.channel, got, wantAddr)
		}
	}

	// Revoking consent after approval is visible right before sending.
	r := rows[len(rows)-1]
	if _, err := e.pool.Exec(t.Context(), `UPDATE channel_consents SET granted = false WHERE user_id = $1 AND channel = $2`,
		r.userID, r.channel); err != nil {
		t.Fatal(err)
	}
	job, _, _ := store.Claim(t.Context(), delivery.ID(r.id), time.Minute)
	if got, err := store.Recipient(t.Context(), job); err != nil || got.Consented {
		t.Errorf("Recipient() after revocation = %+v, %v; want not consented", got, err)
	}
}

func TestDeliveryStore_CampaignCompletesWhenAllTerminal(t *testing.T) {
	e := newCampaignEnv(t)
	c, rows := e.approved(t)
	store := postgres.NewDeliveryStore(e.pool)
	for i, r := range rows {
		if s := campaignStatus(t, e.pool, c.ID); s == "completed" {
			t.Fatalf("campaign completed with %d deliveries left", len(rows)-i)
		}
		job, ok, err := store.Claim(t.Context(), delivery.ID(r.id), time.Minute)
		if err != nil || !ok {
			t.Fatal(err)
		}
		o := delivery.Outcome{Status: delivery.StatusSent, ProviderMessageID: fmt.Sprintf("msg_%d", i)}
		if i%3 == 0 {
			o = delivery.Outcome{Status: delivery.StatusFailed, ErrorCode: delivery.CodeRejected}
		}
		if _, err := store.Record(t.Context(), job, o); err != nil {
			t.Fatal(err)
		}
	}
	if s := campaignStatus(t, e.pool, c.ID); s != "completed" {
		t.Fatalf("campaign status = %s, want completed", s)
	}
	evs := events(t, e.pool, c.ID)
	last := evs[len(evs)-1]
	if last.action != "completed" || last.from != "sending" || last.to != "completed" || last.actor != "system" {
		t.Errorf("last event = %+v", last)
	}
	// The sweep is idempotent.
	if n, err := store.CompleteFinishedCampaigns(t.Context()); err != nil || n != 0 {
		t.Errorf("CompleteFinishedCampaigns() = %d, %v; want 0", n, err)
	}
	got, err := e.svc.Get(t.Context(), c.ID)
	if err != nil || got.Status != campaign.StatusCompleted || got.ApprovedBy == "" {
		t.Errorf("Get() = %+v, %v; want completed and still approved", got.Status, err)
	}
}

func TestDeliveryStore_SweepCompletesCampaignsWithNothingLeft(t *testing.T) {
	e := newCampaignEnv(t)
	c, _ := e.approved(t)
	store := postgres.NewDeliveryStore(e.pool)
	if n, _ := store.CompleteFinishedCampaigns(t.Context()); n != 0 {
		t.Fatalf("completed a campaign with queued deliveries")
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE deliveries SET status = 'failed', last_error_code = 'x' WHERE campaign_id = $1`, string(c.ID)); err != nil {
		t.Fatal(err)
	}
	if n, err := store.CompleteFinishedCampaigns(t.Context()); err != nil || n != 1 {
		t.Fatalf("CompleteFinishedCampaigns() = %d, %v; want 1", n, err)
	}
	if s := campaignStatus(t, e.pool, c.ID); s != "completed" {
		t.Errorf("status = %s", s)
	}
}

func TestDeliveryStore_OutboxRelaySemantics(t *testing.T) {
	e := newCampaignEnv(t)
	_, rows := e.approved(t)
	store := postgres.NewDeliveryStore(e.pool)

	// A failed publish leaves every message pending.
	_, err := store.PublishPending(t.Context(), 100, func(context.Context, []delivery.OutboxMessage) error {
		return errors.New("broker down")
	})
	if err == nil {
		t.Fatal("PublishPending() error = nil, want the publish error")
	}
	if n := count(t, e.pool, `SELECT count(*) FROM outbox WHERE published_at IS NULL`); n != len(rows) {
		t.Fatalf("pending = %d, want %d", n, len(rows))
	}

	// Concurrent relays split the rows; none is published twice.
	var (
		mu   sync.Mutex
		seen []int64
		wg   sync.WaitGroup
	)
	publish := func(_ context.Context, msgs []delivery.OutboxMessage) error {
		time.Sleep(20 * time.Millisecond) // hold the row locks
		mu.Lock()
		defer mu.Unlock()
		for _, m := range msgs {
			seen = append(seen, m.ID)
			var p delivery.JobPayload
			if err := json.Unmarshal(m.Payload, &p); err != nil || p.DeliveryID != m.Key {
				t.Errorf("message %d payload %s does not name its key %s", m.ID, m.Payload, m.Key)
			}
		}
		return nil
	}
	for range 4 {
		wg.Go(func() {
			for {
				n, err := store.PublishPending(t.Context(), 3, publish)
				if err != nil {
					t.Error(err)
				}
				if n == 0 {
					return
				}
			}
		})
	}
	wg.Wait()
	sort.Slice(seen, func(i, j int) bool { return seen[i] < seen[j] })
	if len(seen) != len(rows) || len(slices.Compact(slices.Clone(seen))) != len(seen) {
		t.Errorf("published ids %v, want each of %d rows once", seen, len(rows))
	}
	if n := count(t, e.pool, `SELECT count(*) FROM outbox WHERE published_at IS NULL`); n != 0 {
		t.Errorf("pending after relay = %d", n)
	}
	if n, err := store.PurgePublishedOutbox(t.Context(), 0); err != nil || n != len(rows) {
		t.Errorf("PurgePublishedOutbox() = %d, %v", n, err)
	}
}

func TestTokenBucket_SharedWindowAcrossInstances(t *testing.T) {
	e := newCampaignEnv(t)
	const rate, burst = 20, 5
	var (
		mu     sync.Mutex
		grants []time.Time
		wg     sync.WaitGroup
	)
	ctx, cancel := context.WithTimeout(t.Context(), 2500*time.Millisecond)
	defer cancel()
	// Four "workers", each with its own bucket instance and several
	// goroutines, hammer the same channel row.
	for range 4 {
		b := postgres.NewTokenBucket(e.pool, rate, burst)
		for range 4 {
			wg.Go(func() {
				for ctx.Err() == nil {
					ok, at, err := b.TakeAt(ctx, content.ChannelSMS)
					if err != nil {
						if ctx.Err() == nil {
							t.Error(err)
						}
						return
					}
					if ok {
						mu.Lock()
						grants = append(grants, at)
						mu.Unlock()
					} else {
						time.Sleep(5 * time.Millisecond)
					}
				}
			})
		}
	}
	wg.Wait()
	sort.Slice(grants, func(i, j int) bool { return grants[i].Before(grants[j]) })
	maxInWindow := 0
	for i := range grants {
		j := sort.Search(len(grants), func(k int) bool { return grants[k].After(grants[i].Add(time.Second)) })
		// grants[i:j] lie within [t_i, t_i + 1s]: at most rate·1s + burst.
		maxInWindow = max(maxInWindow, j-i)
	}
	if maxInWindow > rate+burst {
		t.Errorf("max grants in a 1s window = %d, want <= %d", maxInWindow, rate+burst)
	}
	if maxInWindow < rate {
		t.Errorf("max grants in a 1s window = %d: the limiter is far below its rate", maxInWindow)
	}
	// Total over the run is bounded by burst + rate·elapsed.
	if len(grants) > 0 {
		elapsed := grants[len(grants)-1].Sub(grants[0]).Seconds()
		if bound := float64(burst) + rate*elapsed + 1; float64(len(grants)) > bound {
			t.Errorf("granted %d tokens in %.2fs, bound %.1f", len(grants), elapsed, bound)
		}
	}
	// Channels have separate buckets.
	b := postgres.NewTokenBucket(e.pool, rate, burst)
	if wait, err := b.Take(t.Context(), content.ChannelEmail); err != nil || wait != 0 {
		t.Errorf("email Take() = %v, %v; want a token from its own bucket", wait, err)
	}
}

func TestTokenBucket_ReportsWait(t *testing.T) {
	e := newCampaignEnv(t)
	b := postgres.NewTokenBucket(e.pool, 10, 1)
	// Drain the bucket, then the next take must wait about 1/rate.
	for {
		wait, err := b.Take(t.Context(), content.ChannelSMS)
		if err != nil {
			t.Fatal(err)
		}
		if wait > 0 {
			if wait > 100*time.Millisecond {
				t.Errorf("wait = %v, want at most 1/rate = 100ms", wait)
			}
			return
		}
	}
}
