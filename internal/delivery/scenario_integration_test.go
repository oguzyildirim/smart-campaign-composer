//go:build integration

package delivery_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
	"golang.org/x/sync/errgroup"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/delivery"
	"github.com/oguzyildirim/smart-campaign-composer/internal/mockprovider"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/kafka"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/postgres"
	"github.com/oguzyildirim/smart-campaign-composer/internal/testutil/kafkatest"
)

// Failure scenarios over the example campaign: provider faults, consent
// changes, the shared rate limit, duplicate and crash paths, and worker
// shutdown. The provider-side request log is the ground truth.

// providerRequest is one send request as the mock provider received it.
type providerRequest struct {
	key, channel string
	at           time.Time
	status       int // 0: the client hung up before a response
	retryAfter   time.Duration
}

// requestLog records every send request in front of the mock provider.
type requestLog struct {
	mu   sync.Mutex
	reqs []providerRequest
}

func (l *requestLog) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		at := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		req := providerRequest{key: r.Header.Get("Idempotency-Key"), channel: path.Base(r.URL.Path), at: at, status: sw.status}
		if s, err := strconv.Atoi(sw.Header().Get("Retry-After")); err == nil {
			req.retryAfter = time.Duration(s) * time.Second
		}
		l.mu.Lock()
		l.reqs = append(l.reqs, req)
		l.mu.Unlock()
	})
}

// byKey returns each idempotency key's requests in arrival order.
func (l *requestLog) byKey() map[string][]providerRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string][]providerRequest{}
	for _, r := range l.reqs {
		out[r.key] = append(out[r.key], r)
	}
	for _, rs := range out {
		slices.SortFunc(rs, func(a, b providerRequest) int { return a.at.Compare(b.at) })
	}
	return out
}

// maxPerSecond returns the most requests channel received in any
// one-second sliding window.
func (l *requestLog) maxPerSecond(channel string) int {
	l.mu.Lock()
	var ts []time.Time
	for _, r := range l.reqs {
		if r.channel == channel {
			ts = append(ts, r.at)
		}
	}
	l.mu.Unlock()
	slices.SortFunc(ts, time.Time.Compare)
	best, lo := 0, 0
	for hi := range ts {
		for !ts[lo].After(ts[hi].Add(-time.Second)) {
			lo++
		}
		best = max(best, hi-lo+1)
	}
	return best
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// checkOnePerRecipient asserts that the provider holds at most one message
// per idempotency key and per (channel, recipient), and only for this
// campaign's deliveries.
func checkOnePerRecipient(t *testing.T, e e2e) {
	t.Helper()
	ids := map[string]bool{}
	for _, r := range e.rows(t) {
		ids[r.id] = true
	}
	resp, err := http.Get(e.mock.URL + "/v1/messages?limit=1000")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list struct {
		Messages []mockprovider.Delivered `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	type recipient struct{ channel, to string }
	keys, recipients := map[string]int{}, map[recipient]int{}
	for _, m := range list.Messages {
		keys[m.IdempotencyKey]++
		recipients[recipient{m.Channel, m.To}]++
		if !ids[m.IdempotencyKey] {
			t.Errorf("provider message under key %s, which is not a delivery of the campaign", m.IdempotencyKey)
		}
	}
	for k, n := range keys {
		if n > 1 {
			t.Errorf("key %s delivered %d times", k, n)
		}
	}
	for r, n := range recipients {
		if n > 1 {
			t.Errorf("%d messages to one %s recipient", n, r.channel)
		}
	}
}

// drainWith processes due deliveries concurrently, round-robin over
// procs, until the campaign completes. Expired leases are recovered
// between rounds.
func (e e2e) drainWith(t *testing.T, concurrency int, procs ...*delivery.Processor) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for e.campaignStatus(t) != "completed" {
		if time.Now().After(deadline) {
			t.Fatalf("campaign did not complete; deliveries: %+v", e.rows(t))
		}
		var g errgroup.Group
		g.SetLimit(concurrency * len(procs))
		for i, r := range e.rows(t) {
			if r.status == "queued" || r.status == "retrying" {
				g.Go(func() error { return procs[i%len(procs)].Process(t.Context(), delivery.ID(r.id)) })
			}
		}
		if err := g.Wait(); err != nil {
			t.Fatal(err)
		}
		if _, err := e.store.RecoverExpiredLeases(t.Context(), 1000); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// count runs a count query.
func count(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// spacingPolicy has a cap above Retry-After (1s), so Retry-After is
// honoured in full; halfRand makes every backoff exactly half its ceiling.
var (
	spacingPolicy = delivery.RetryPolicy{BaseDelay: 50 * time.Millisecond, Factor: 2, MaxDelay: 1500 * time.Millisecond}
	halfRand      = func() float64 { return 0.5 }
)

// checkSpacing asserts, per idempotency key, that the provider saw at
// most max_attempts requests and that each retry came no earlier than the
// backoff and the previous response's Retry-After required.
func checkSpacing(t *testing.T, e e2e, policy delivery.RetryPolicy, rnd float64) {
	t.Helper()
	const tolerance = 5 * time.Millisecond
	for key, reqs := range e.requests.byKey() {
		if len(reqs) > 5 {
			t.Errorf("key %s: %d provider requests, max attempts 5", key, len(reqs))
		}
		for i := 1; i < len(reqs); i++ {
			want := policy.Delay(i, reqs[i-1].retryAfter, rnd)
			if gap := reqs[i].at.Sub(reqs[i-1].at); gap < want-tolerance {
				t.Errorf("key %s: attempt %d came %v after a %d response, want >= %v", key, i+1, gap, reqs[i-1].status, want)
			}
		}
	}
}

func TestScenario_ProviderFailures(t *testing.T) {
	tests := []struct {
		name   string
		faults mockprovider.Config
		check  func(t *testing.T, e e2e, st map[string]mockprovider.ChannelStats)
	}{
		{
			name:   "5xx bursts",
			faults: mockprovider.Config{ServerErrorRate: 0.5},
			check: func(t *testing.T, _ e2e, st map[string]mockprovider.ChannelStats) {
				if st["sms"].ServerErrors+st["email"].ServerErrors == 0 {
					t.Error("no 5xx was injected")
				}
			},
		},
		{
			name:   "429 storm with Retry-After",
			faults: mockprovider.Config{RateLimitRate: 0.5, RetryAfter: time.Second},
			check: func(t *testing.T, _ e2e, st map[string]mockprovider.ChannelStats) {
				if st["sms"].RateLimited+st["email"].RateLimited == 0 {
					t.Error("no 429 was injected")
				}
			},
		},
		{
			// Every first attempt outlasts the client's 300ms timeout, but
			// the provider still accepts it; the retry is answered from
			// the idempotency record.
			name:   "timeouts",
			faults: mockprovider.Config{LatencyMin: 400 * time.Millisecond, LatencyMax: 400 * time.Millisecond},
			check: func(t *testing.T, e e2e, _ map[string]mockprovider.ChannelStats) {
				for _, r := range e.rows(t) {
					if r.status != "sent" || r.attempts != 2 {
						t.Errorf("delivery %+v, want sent on attempt 2", r)
					}
				}
			},
		},
		{
			name:   "accepted but response lost",
			faults: mockprovider.Config{LostResponseRate: 0.5, LostResponseDelay: time.Second},
			check: func(t *testing.T, _ e2e, st map[string]mockprovider.ChannelStats) {
				if lost := st["sms"].LostResponses + st["email"].LostResponses; lost == 0 ||
					st["sms"].DuplicatesSuppressed+st["email"].DuplicatesSuppressed != lost {
					t.Errorf("stats = %+v, want every lost response resent once and suppressed", st)
				}
			},
		},
		{
			name:   "permanent 4xx",
			faults: mockprovider.Config{PermanentFailureRate: 0.3},
			check: func(t *testing.T, e e2e, st map[string]mockprovider.ChannelStats) {
				rejected := 0
				for _, r := range e.rows(t) {
					if r.status == "failed" {
						rejected++
						if r.code != delivery.CodeRejected || r.attempts != 1 {
							t.Errorf("delivery %+v, want failed(provider_rejected) after one attempt", r)
						}
					}
				}
				if rejected == 0 || int64(rejected) != st["sms"].PermanentFailures+st["email"].PermanentFailures {
					t.Errorf("%d rejected deliveries, provider stats %+v", rejected, st)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newE2E(t, tt.faults, 95, 5)
			proc := e.processorWith(delivery.ProcessorConfig{Lease: 5 * time.Second, Policy: spacingPolicy, Rand: halfRand})
			e.drainWith(t, 16, proc)
			checkInvariants(t, e)
			checkSpacing(t, e, spacingPolicy, 0.5)
			tt.check(t, e, e.stats(t).Channels)
		})
	}
}

func (e e2e) processorWith(cfg delivery.ProcessorConfig) *delivery.Processor {
	return delivery.NewProcessor(e.store, e.limiter, e.client, cfg, logger)
}

func TestScenario_Consent(t *testing.T) {
	e := newE2E(t, mockprovider.Config{}, 95, 5)

	// No consent at approval: no delivery row.
	if n := count(t, e.pool, `SELECT count(*) FROM deliveries d WHERE d.campaign_id = $1 AND NOT EXISTS (
			SELECT 1 FROM channel_consents c WHERE c.user_id = d.user_id AND c.channel = d.channel AND c.granted)`,
		string(e.campaign.ID)); n != 0 {
		t.Fatalf("%d deliveries for users without consent on the channel", n)
	}

	// Recipients reachable on one channel only, with the other channel's
	// address, which must never reach the provider.
	rs, err := e.pool.Query(t.Context(), `SELECT CASE WHEN min(d.channel) = 'sms' THEN u.email ELSE u.phone END
		FROM deliveries d JOIN users u ON u.id = d.user_id
		WHERE d.campaign_id = $1 GROUP BY u.id HAVING count(*) = 1`, string(e.campaign.ID))
	if err != nil {
		t.Fatal(err)
	}
	var unconsented []string
	for rs.Next() {
		var addr string
		if err := rs.Scan(&addr); err != nil {
			t.Fatal(err)
		}
		unconsented = append(unconsented, addr)
	}
	rs.Close()
	if len(unconsented) == 0 {
		t.Fatal("the seed has no single-channel recipient in the example segment")
	}

	// Consent revoked after approval: those deliveries fail unsent.
	rows := e.rows(t)
	revoked := map[string]bool{}
	for _, r := range rows[:5] {
		revoked[r.id] = true
		if _, err := e.pool.Exec(t.Context(), `UPDATE channel_consents SET granted = false, updated_at = now()
			WHERE user_id = $1 AND channel = $2`, r.userID, r.channel); err != nil {
			t.Fatal(err)
		}
	}

	e.drainWith(t, 16, e.proc)
	checkInvariants(t, e)

	reqs := e.requests.byKey()
	for _, r := range e.rows(t) {
		if !revoked[r.id] {
			continue
		}
		if r.status != "failed" || r.code != delivery.CodeConsentRevoked {
			t.Errorf("revoked delivery %+v, want failed(consent_revoked)", r)
		}
		if n := len(reqs[r.id]); n != 0 {
			t.Errorf("revoked delivery %s reached the provider %d times", r.id, n)
		}
	}
	resp, err := http.Get(e.mock.URL + "/v1/messages?limit=1000")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list struct {
		Messages []mockprovider.Delivered `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	for _, m := range list.Messages {
		if slices.Contains(unconsented, m.To) {
			t.Errorf("a %s message went to a channel the recipient did not consent to", m.Channel)
		}
	}
}

// TestScenario_RateLimitAcrossWorkers runs four worker instances, each
// with its own connection pool and limiter over the shared bucket, under
// 429s that force retries. Every one-second window at the provider holds
// at most rate + burst requests per channel, retries included.
func TestScenario_RateLimitAcrossWorkers(t *testing.T) {
	const rate, burst = 20, 5
	// About 140 deliveries per channel: several seconds at the limit.
	e := newE2EWithUsers(t, 3000, mockprovider.Config{RateLimitRate: 0.2, RetryAfter: time.Second}, rate, burst)
	var procs []*delivery.Processor
	for range 4 {
		pool, err := postgres.NewPool(t.Context(), e.dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		limiter := delivery.NewLimiter(postgres.NewTokenBucket(pool, rate, burst))
		procs = append(procs, delivery.NewProcessor(postgres.NewDeliveryStore(pool), limiter, e.client,
			delivery.ProcessorConfig{Lease: 30 * time.Second, Policy: testPolicy}, logger))
	}
	e.drainWith(t, 4, procs...)
	checkInvariants(t, e)

	st := e.stats(t).Channels
	if st["sms"].RateLimited+st["email"].RateLimited == 0 {
		t.Error("no 429 was injected, so no retry was rate limited")
	}
	for _, ch := range []string{"sms", "email"} {
		got := e.requests.maxPerSecond(ch)
		if got > rate+burst {
			t.Errorf("%s: %d requests in one second, limit %d", ch, got, rate+burst)
		}
		// Sixteen concurrent senders saturate the limit; a much lower
		// peak would mean the test did not exercise it.
		if got < rate {
			t.Errorf("%s: peak %d requests per second, want the limiter saturated (>= %d)", ch, got, rate)
		}
	}
}

// crashingStore loses the outcome of the first attempt of the given
// deliveries, as if the worker died between the provider's acceptance and
// the database update.
type crashingStore struct {
	delivery.Store
	mu    sync.Mutex
	crash map[delivery.ID]bool
}

var errCrashed = errors.New("worker crashed before recording")

func (s *crashingStore) Record(ctx context.Context, job delivery.Job, o delivery.Outcome) (bool, error) {
	s.mu.Lock()
	crash := s.crash[job.ID]
	delete(s.crash, job.ID)
	s.mu.Unlock()
	if crash {
		return false, errCrashed
	}
	return s.Store.Record(ctx, job, o)
}

func TestScenario_CrashAfterProviderAccepted(t *testing.T) {
	e := newE2E(t, mockprovider.Config{}, 95, 5)
	targets := e.rows(t)[:5]
	store := &crashingStore{Store: e.store, crash: map[delivery.ID]bool{}}
	for _, r := range targets {
		store.crash[delivery.ID(r.id)] = true
	}
	crashing := delivery.NewProcessor(store, e.limiter, e.client,
		delivery.ProcessorConfig{Lease: 500 * time.Millisecond, Policy: testPolicy}, logger)
	for _, r := range targets {
		if err := crashing.Process(t.Context(), delivery.ID(r.id)); !errors.Is(err, errCrashed) {
			t.Fatalf("process %s: %v, want the crash", r.id, err)
		}
		if got := e.delivery(t, r.id); got.status != "processing" {
			t.Fatalf("after the crash: %+v, want still processing", got)
		}
	}

	time.Sleep(600 * time.Millisecond) // past the lease
	e.drainWith(t, 16, e.proc)
	checkInvariants(t, e)

	reqs := e.requests.byKey()
	for _, r := range targets {
		got := e.delivery(t, r.id)
		if got.status != "sent" || got.attempts != 2 {
			t.Errorf("crashed delivery %+v, want sent on attempt 2", got)
		}
		if n := len(reqs[r.id]); n != 2 {
			t.Errorf("crashed delivery %s: %d provider requests, want 2", r.id, n)
		}
	}
	st := e.stats(t).Channels
	if n := st["sms"].DuplicatesSuppressed + st["email"].DuplicatesSuppressed; n != int64(len(targets)) {
		t.Errorf("provider suppressed %d resends, want %d", n, len(targets))
	}
}

// stallingProvider sends, then holds the response of the first send of
// target until release closes: a worker still busy when its lease expires.
type stallingProvider struct {
	delivery.Provider
	target  delivery.ID
	sent    chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *stallingProvider) Send(ctx context.Context, msg delivery.Message) (string, error) {
	id, err := p.Provider.Send(ctx, msg)
	if msg.IdempotencyKey != p.target {
		return id, err
	}
	stall := false
	p.once.Do(func() { stall = true })
	if stall {
		close(p.sent)
		<-p.release
	}
	return id, err
}

func TestScenario_LeaseExpiresWhileSending(t *testing.T) {
	e := newE2E(t, mockprovider.Config{}, 95, 5)
	target := e.rows(t)[0]
	slow := &stallingProvider{Provider: e.client, target: delivery.ID(target.id), sent: make(chan struct{}), release: make(chan struct{})}
	stalled := delivery.NewProcessor(e.store, e.limiter, slow,
		delivery.ProcessorConfig{Lease: 300 * time.Millisecond, Policy: testPolicy}, logger)

	done := make(chan error, 1)
	go func() { done <- stalled.Process(t.Context(), delivery.ID(target.id)) }()
	<-slow.sent
	time.Sleep(400 * time.Millisecond) // the first worker's lease expires
	if n, err := e.store.RecoverExpiredLeases(t.Context(), 10); err != nil || n != 1 {
		t.Fatalf("recovered %d leases (%v), want 1", n, err)
	}
	if err := e.proc.Process(t.Context(), delivery.ID(target.id)); err != nil {
		t.Fatal(err)
	}
	close(slow.release)
	if err := <-done; err != nil {
		t.Errorf("stalled worker: %v, want a lost claim to be no error", err)
	}

	got := e.delivery(t, target.id)
	if got.status != "sent" || got.attempts != 2 {
		t.Errorf("delivery %+v, want sent by the second claim (attempt 2)", got)
	}
	if n := len(e.requests.byKey()[target.id]); n != 2 {
		t.Errorf("%d provider requests, want 2", n)
	}
	st := e.stats(t).Channels[target.channel]
	if st.Delivered != 1 || st.DuplicatesSuppressed != 1 {
		t.Errorf("provider stats %+v, want one delivery and the second send suppressed", st)
	}
	checkOnePerRecipient(t, e)
}

// TestScenario_GracefulShutdownAndRestart stops every worker mid-campaign,
// then starts new ones: the campaign completes with no lost or duplicate
// deliveries.
func TestScenario_GracefulShutdownAndRestart(t *testing.T) {
	const rate, burst = 20, 5
	e := newE2EWithUsers(t, 2000, mockprovider.Config{ServerErrorRate: 0.05, LatencyMin: 20 * time.Millisecond, LatencyMax: 60 * time.Millisecond}, rate, burst)
	k := startKafka(t)
	total := len(e.rows(t))

	ctx, cancel := context.WithCancel(t.Context())
	wait := startWorkers(ctx, t, e, k, 2)
	deadline := time.Now().Add(60 * time.Second)
	for count(t, e.pool, `SELECT count(*) FROM deliveries WHERE status = 'sent'`) < total/4 {
		if time.Now().After(deadline) {
			t.Fatal("workers did not start sending")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if s := e.campaignStatus(t); s == "completed" {
		t.Fatal("the campaign completed before the shutdown; nothing was interrupted")
	}
	if n := count(t, e.pool, `SELECT count(*) FROM deliveries WHERE status = 'processing'`); n != 0 {
		t.Errorf("%d deliveries left processing after a graceful shutdown", n)
	}

	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	wait = startWorkers(ctx, t, e, k, 2)
	deadline = time.Now().Add(120 * time.Second)
	for e.campaignStatus(t) != "completed" {
		if time.Now().After(deadline) {
			t.Fatalf("campaign did not complete after the restart; deliveries: %+v", e.rows(t))
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	if err := wait(); err != nil {
		t.Errorf("workers: %v", err)
	}
	checkInvariants(t, e)
	for _, ch := range []string{"sms", "email"} {
		if got := e.requests.maxPerSecond(ch); got > rate+burst {
			t.Errorf("%s: %d requests in one second, limit %d", ch, got, rate+burst)
		}
	}
}

// testKafka is a broker with the delivery topics and a shared producer.
type testKafka struct {
	brokers  []string
	producer *kgo.Client
	topics   []string
}

func startKafka(t *testing.T) testKafka {
	t.Helper()
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
	return testKafka{brokers: brokers, producer: producer, topics: topics}
}

// startWorkers runs n workers' worth of consumers, outbox relays, and
// maintenance loops until ctx ends. Consumers leave the group when they
// stop, so a later set takes over their partitions at once. wait returns
// the first loop error.
func startWorkers(ctx context.Context, t *testing.T, e e2e, k testKafka, n int) (wait func() error) {
	t.Helper()
	g, gctx := errgroup.WithContext(ctx)
	for i := range n {
		consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
			Brokers: k.brokers, Group: "e2e", Topics: k.topics, Concurrency: 4, MaxPollRecords: 16, Grace: time.Second,
		}, e.proc.Handle, logger)
		if err != nil {
			t.Fatal(err)
		}
		g.Go(func() error {
			defer consumer.Close()
			return consumer.Run(gctx)
		})
		g.Go(func() error {
			return delivery.RunRelay(gctx, e.store, kafka.NewPublisher(k.producer),
				delivery.RelayConfig{BatchSize: 10 + i, PollInterval: 50 * time.Millisecond}, logger)
		})
		g.Go(func() error {
			return delivery.RunMaintenance(gctx, e.store,
				delivery.MaintenanceConfig{Interval: 100 * time.Millisecond, BatchSize: 50, OutboxRetention: time.Hour}, logger)
		})
	}
	return g.Wait
}
