//go:build integration

package delivery_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/delivery"
	"github.com/oguzyildirim/smart-campaign-composer/internal/llm/mockllm"
	"github.com/oguzyildirim/smart-campaign-composer/internal/mockprovider"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/kafka"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/postgres"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/provider"
	"github.com/oguzyildirim/smart-campaign-composer/internal/seed"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
	"github.com/oguzyildirim/smart-campaign-composer/internal/testutil/kafkatest"
	"github.com/oguzyildirim/smart-campaign-composer/internal/testutil/pgtest"
)

const exampleBrief = "Son 30 günde sepetine ürün ekleyip alışveriş yapmayan İstanbul'daki kullanıcılara %30 indirim hatırlatması gönder. Samimi ama kısa olsun."

var logger = slog.New(slog.DiscardHandler)

// e2e is an approved example campaign over seeded fake users, with a mock
// provider configured by the test.
type e2e struct {
	pool     *pgxpool.Pool
	store    *postgres.DeliveryStore
	campaign campaign.Campaign
	mock     *httptest.Server
	proc     *delivery.Processor
	limiter  *delivery.Limiter
	client   *provider.Client
}

// testPolicy keeps retries fast; its cap also bounds Retry-After.
var testPolicy = delivery.RetryPolicy{BaseDelay: 20 * time.Millisecond, Factor: 2, MaxDelay: 200 * time.Millisecond}

func (e e2e) processor(policy delivery.RetryPolicy) *delivery.Processor {
	return delivery.NewProcessor(e.store, e.limiter, e.client, delivery.ProcessorConfig{Lease: 5 * time.Second, Policy: policy}, logger)
}

// newE2E seeds users, approves the example campaign through the real
// service, and wires a processor to a mock provider with faults.
func newE2E(t *testing.T, faults mockprovider.Config, rate, burst int) e2e {
	t.Helper()
	dsn := pgtest.Start(t)
	pgtest.Migrate(t, dsn)
	pool, err := postgres.NewPool(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	now := time.Now()
	if _, err := seed.Load(t.Context(), pool, seed.Generate(seed.Options{UserCount: 400, RandomSeed: 1, Now: now})); err != nil {
		t.Fatal(err)
	}

	audience := postgres.NewAudienceRepository(pool)
	clock := func() time.Time { return now }
	gen := campaign.NewGenerator(mockllm.New(), segment.NewService(audience, clock), campaign.GeneratorConfig{
		SegmentTimeout: 5 * time.Second, DraftsTimeout: 5 * time.Second, MaxAttempts: 1,
	}, logger)
	svc := campaign.NewService(gen, postgres.NewCampaignStore(pool), audience, campaign.ServiceConfig{
		GenerationTimeout: 10 * time.Second, PreviewSamples: 1, DeliveryMaxAttempts: 5,
	}, clock, logger)
	c, err := svc.Create(t.Context(), exampleBrief)
	if err != nil {
		t.Fatal(err)
	}
	g, err := svc.BeginGeneration(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c, err = g.Run(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if c, err = svc.Approve(t.Context(), c.ID, campaign.Approval{Version: c.Version, ContentHash: c.ContentHash, ApprovedBy: "E2E"}); err != nil {
		t.Fatal(err)
	}

	if faults.Seed == 0 {
		faults.Seed = 11
	}
	mock := httptest.NewServer(mockprovider.New(faults, logger).Handler())
	t.Cleanup(mock.Close)
	client, err := provider.New(mock.URL, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	e := e2e{pool: pool, store: postgres.NewDeliveryStore(pool), campaign: c, mock: mock, client: client,
		limiter: delivery.NewLimiter(postgres.NewTokenBucket(pool, rate, burst))}
	e.proc = e.processor(testPolicy)
	return e
}

type dbRow struct {
	id, channel, status, code string
	userID                    int64
	attempts                  int
}

func (e e2e) rows(t *testing.T) []dbRow {
	t.Helper()
	rs, err := e.pool.Query(t.Context(), `SELECT id::text, channel, status, coalesce(last_error_code, ''), user_id, attempts
		FROM deliveries WHERE campaign_id = $1 ORDER BY id`, string(e.campaign.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []dbRow
	for rs.Next() {
		var r dbRow
		if err := rs.Scan(&r.id, &r.channel, &r.status, &r.code, &r.userID, &r.attempts); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		t.Fatal("the example campaign has no deliveries")
	}
	return out
}

func (e e2e) delivery(t *testing.T, id string) dbRow {
	t.Helper()
	for _, r := range e.rows(t) {
		if r.id == id {
			return r
		}
	}
	t.Fatalf("no delivery %s", id)
	return dbRow{}
}

func (e e2e) stats(t *testing.T) mockprovider.Stats {
	t.Helper()
	resp, err := http.Get(e.mock.URL + "/v1/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s mockprovider.Stats
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (e e2e) campaignStatus(t *testing.T) string {
	t.Helper()
	var s string
	if err := e.pool.QueryRow(t.Context(), `SELECT status FROM campaigns WHERE id = $1`, string(e.campaign.ID)).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// drain processes deliveries without Kafka, running the scheduler between
// rounds, until the campaign completes.
func (e e2e) drain(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for e.campaignStatus(t) != "completed" {
		if time.Now().After(deadline) {
			t.Fatalf("campaign did not complete; deliveries: %+v", e.rows(t))
		}
		for _, r := range e.rows(t) {
			if r.status == "queued" || r.status == "retrying" {
				if err := e.proc.Process(t.Context(), delivery.ID(r.id)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if _, err := e.store.RecoverExpiredLeases(t.Context(), 1000); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// checkInvariants asserts the delivery guarantees after a run.
func checkInvariants(t *testing.T, e e2e) {
	t.Helper()
	sent := map[string]int{}
	for _, r := range e.rows(t) {
		switch {
		case r.status == "sent":
			sent[r.channel]++
		case r.status == "failed" && (r.code == delivery.CodeRetriesExhausted || r.code == delivery.CodeRejected):
		default:
			t.Errorf("delivery %+v: want sent, or failed by a permanent error or exhausted retries", r)
		}
		if r.attempts > 5 {
			t.Errorf("delivery %s made %d attempts, max 5", r.id, r.attempts)
		}
	}
	for ch, st := range e.stats(t).Channels {
		if st.Delivered != int64(sent[ch]) {
			t.Errorf("%s: provider delivered %d, database marks %d sent", ch, st.Delivered, sent[ch])
		}
		if st.DuplicateContent != 0 {
			t.Errorf("%s: %d recipients got the same message twice", ch, st.DuplicateContent)
		}
	}
}

func TestE2E_TransientFailuresAreRetriedUntilSent(t *testing.T) {
	e := newE2E(t, mockprovider.Config{
		ServerErrorRate: 0.3, LostResponseRate: 0.1, LostResponseDelay: 500 * time.Millisecond,
	}, 95, 5)
	e.drain(t)
	checkInvariants(t, e)

	retried := 0
	for _, r := range e.rows(t) {
		if r.status == "sent" && r.attempts > 1 {
			retried++
		}
	}
	if retried == 0 {
		t.Error("no delivery needed a retry; the fault injection did not run")
	}
	if st := e.stats(t).Channels; st["sms"].ServerErrors+st["email"].ServerErrors == 0 {
		t.Error("the mock provider returned no 5xx")
	}
}

func TestE2E_PermanentFailureIsNotRetried(t *testing.T) {
	e := newE2E(t, mockprovider.Config{PermanentFailureRate: 1}, 95, 5)
	target := e.rows(t)[0]
	if err := e.proc.Process(t.Context(), delivery.ID(target.id)); err != nil {
		t.Fatal(err)
	}
	got := e.delivery(t, target.id)
	if got.status != "failed" || got.code != delivery.CodeRejected || got.attempts != 1 {
		t.Errorf("delivery = %+v, want failed(provider_rejected) after 1 attempt", got)
	}
	if n, _ := e.store.RequeueDue(t.Context(), 100); n != 0 {
		t.Errorf("requeued %d deliveries after a permanent failure", n)
	}
}

func TestE2E_LostResponseIsRetriedWithTheSameKey(t *testing.T) {
	e := newE2E(t, mockprovider.Config{LostResponseRate: 1, LostResponseDelay: time.Second}, 95, 5)
	target := e.rows(t)[0]

	if err := e.proc.Process(t.Context(), delivery.ID(target.id)); err != nil {
		t.Fatal(err)
	}
	if got := e.delivery(t, target.id); got.status != "retrying" || got.code != delivery.CodeTimeout {
		t.Fatalf("after the lost response: %+v, want retrying(provider_timeout)", got)
	}
	time.Sleep(250 * time.Millisecond) // past the capped backoff
	if err := e.proc.Process(t.Context(), delivery.ID(target.id)); err != nil {
		t.Fatal(err)
	}
	if got := e.delivery(t, target.id); got.status != "sent" || got.attempts != 2 {
		t.Fatalf("after the retry: %+v, want sent on attempt 2", got)
	}
	st := e.stats(t).Channels[target.channel]
	if st.Delivered != 1 || st.DuplicatesSuppressed != 1 {
		t.Errorf("provider stats = %+v, want 1 delivered and the retry suppressed", st)
	}
}

func TestE2E_ConsentRevokedAfterApprovalIsNotSent(t *testing.T) {
	e := newE2E(t, mockprovider.Config{}, 95, 5)
	target := e.rows(t)[0]
	if _, err := e.pool.Exec(t.Context(), `UPDATE channel_consents SET granted = false, updated_at = now()
		WHERE user_id = $1 AND channel = $2`, target.userID, target.channel); err != nil {
		t.Fatal(err)
	}
	if err := e.proc.Process(t.Context(), delivery.ID(target.id)); err != nil {
		t.Fatal(err)
	}
	if got := e.delivery(t, target.id); got.status != "failed" || got.code != delivery.CodeConsentRevoked {
		t.Errorf("delivery = %+v, want failed(consent_revoked)", got)
	}
	if st := e.stats(t).Channels[target.channel]; st.Received != 0 {
		t.Errorf("provider received %d requests, want none", st.Received)
	}
}

func TestE2E_RetryAfterIsALowerBound(t *testing.T) {
	e := newE2E(t, mockprovider.Config{RateLimitRate: 1, RetryAfter: 2 * time.Second}, 95, 5)
	target := e.rows(t)[0]
	// Backoff alone would wait at most 20ms after the first attempt.
	policy := delivery.RetryPolicy{BaseDelay: 20 * time.Millisecond, Factor: 2, MaxDelay: time.Minute}
	if err := e.processor(policy).Process(t.Context(), delivery.ID(target.id)); err != nil {
		t.Fatal(err)
	}
	var (
		status, code string
		wait         float64
	)
	if err := e.pool.QueryRow(t.Context(), `SELECT status, last_error_code,
		extract(epoch FROM next_attempt_at - updated_at)::float8 FROM deliveries WHERE id = $1`, target.id).
		Scan(&status, &code, &wait); err != nil {
		t.Fatal(err)
	}
	if status != "retrying" || code != delivery.CodeRateLimited || wait < 1.99 {
		t.Errorf("delivery %s(%s) due in %.3fs, want retrying(provider_rate_limited) after >= 2s", status, code, wait)
	}
}

// TestE2E_PipelineWithKafka runs three workers' worth of relays,
// consumers, and maintenance loops against Kafka and a faulty provider,
// then replays every queue message to check that duplicates are absorbed.
func TestE2E_PipelineWithKafka(t *testing.T) {
	const rate, burst = 20, 5
	e := newE2E(t, mockprovider.Config{
		ServerErrorRate: 0.1, RateLimitRate: 0.05, RetryAfter: time.Second, PermanentFailureRate: 0.02,
		LostResponseRate: 0.05, LostResponseDelay: 500 * time.Millisecond, MaxPerSecond: 100,
	}, rate, burst)
	brokers := kafkatest.Start(t)
	producer, err := kafka.NewClient(brokers, kafka.ProducerOpts()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(producer.Close)
	topics := []string{delivery.Topic(content.ChannelSMS), delivery.Topic(content.ChannelEmail)}
	if err := kafka.EnsureTopics(t.Context(), producer, 3, topics...); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	g, gctx := errgroup.WithContext(ctx)
	for i := range 3 {
		consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
			Brokers: brokers, Group: "e2e", Topics: topics, Concurrency: 4, MaxPollRecords: 16, Grace: time.Second,
		}, e.proc.Handle, logger)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(consumer.Close)
		g.Go(func() error { return consumer.Run(gctx) })
		g.Go(func() error {
			return delivery.RunRelay(gctx, e.store, kafka.NewPublisher(producer),
				delivery.RelayConfig{BatchSize: 10 + i, PollInterval: 50 * time.Millisecond}, logger)
		})
		g.Go(func() error {
			return delivery.RunMaintenance(gctx, e.store,
				delivery.MaintenanceConfig{Interval: 100 * time.Millisecond, BatchSize: 50, OutboxRetention: time.Hour}, logger)
		})
	}

	deadline := time.Now().Add(120 * time.Second)
	for e.campaignStatus(t) != "completed" {
		if time.Now().After(deadline) {
			t.Fatalf("campaign did not complete; deliveries: %+v", e.rows(t))
		}
		time.Sleep(100 * time.Millisecond)
	}
	checkInvariants(t, e)
	before := e.stats(t)
	for ch, st := range before.Channels {
		if st.MaxPerSecond > rate+burst {
			t.Errorf("%s: provider saw %d requests in one second, limit %d", ch, st.MaxPerSecond, rate+burst)
		}
	}

	// Replay every queue message: claims reject them all.
	if _, err := e.pool.Exec(t.Context(), `UPDATE outbox SET published_at = NULL`); err != nil {
		t.Fatal(err)
	}
	waitFor := time.Now().Add(30 * time.Second)
	for {
		var pending int
		if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 0 || time.Now().After(waitFor) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(2 * time.Second) // let the consumers process the replay
	after := e.stats(t)
	for ch := range before.Channels {
		if after.Channels[ch].Received != before.Channels[ch].Received {
			t.Errorf("%s: replayed messages reached the provider (%d → %d requests)",
				ch, before.Channels[ch].Received, after.Channels[ch].Received)
		}
	}
	cancel()
	if err := g.Wait(); err != nil {
		t.Errorf("workers: %v", err)
	}
	t.Logf("deliveries: %d; provider stats: %+v", len(e.rows(t)), fmt.Sprint(after.Channels))
}
