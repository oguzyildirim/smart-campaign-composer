//go:build integration

package kafka_test

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/oguzyildirim/smart-campaign-composer/internal/delivery"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/kafka"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/postgres"
	"github.com/oguzyildirim/smart-campaign-composer/internal/testutil/kafkatest"
	"github.com/oguzyildirim/smart-campaign-composer/internal/testutil/pgtest"
)

var discard = slog.New(slog.DiscardHandler)

var topics = []string{"deliveries.sms", "deliveries.email"}

func setupTopics(t *testing.T, brokers []string) *kgo.Client {
	t.Helper()
	client, err := kafka.NewClient(brokers, kafka.ProducerOpts()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	if err := kafka.EnsureTopics(t.Context(), client, 3, topics...); err != nil {
		t.Fatalf("EnsureTopics() error = %v", err)
	}
	// Idempotent.
	if err := kafka.EnsureTopics(t.Context(), client, 3, topics...); err != nil {
		t.Fatalf("second EnsureTopics() error = %v", err)
	}
	return client
}

// collector is a handler that records every value it sees.
type collector struct {
	mu   sync.Mutex
	seen map[string]int
	n    int
}

func (c *collector) handle(_ context.Context, v []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = map[string]int{}
	}
	c.seen[string(v)]++
	c.n++
	return nil
}

func (c *collector) total() (distinct, all int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen), c.n
}

func runConsumer(ctx context.Context, t *testing.T, brokers []string, group string, h kafka.Handler, grace time.Duration) <-chan error {
	t.Helper()
	c, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers: brokers, Group: group, Topics: topics, Concurrency: 4, MaxPollRecords: 16, Grace: grace,
	}, h, discard)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		err := c.Run(ctx)
		c.Close()
		done <- err
	}()
	return done
}

func eventually(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRelay_PublishesEveryOutboxRowAndToleratesRepublish(t *testing.T) {
	brokers := kafkatest.Start(t)
	dsn := pgtest.Start(t)
	pgtest.Migrate(t, dsn)
	pool, err := postgres.NewPool(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	producer := setupTopics(t, brokers)

	const n = 30
	for i := range n {
		id := fmt.Sprintf("0199f0a0-0000-7000-8000-%012d", i)
		if _, err := pool.Exec(t.Context(), `INSERT INTO outbox (topic, key, payload) VALUES ($1, $2, $3)`,
			topics[i%2], id, string(delivery.EncodeJob(delivery.ID(id)))); err != nil {
			t.Fatal(err)
		}
	}
	store := postgres.NewDeliveryStore(pool)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	relayDone := make(chan error, 1)
	go func() {
		relayDone <- delivery.RunRelay(ctx, store, kafka.NewPublisher(producer),
			delivery.RelayConfig{BatchSize: 7, PollInterval: 20 * time.Millisecond}, discard)
	}()
	col := &collector{}
	consumerDone := runConsumer(ctx, t, brokers, "relay-test", col.handle, time.Second)

	eventually(t, 60*time.Second, func() bool { d, _ := col.total(); return d == n }, "not every outbox row reached the consumer")

	// A relay crash after publishing but before marking rows republishes them.
	if _, err := pool.Exec(t.Context(), `UPDATE outbox SET published_at = NULL WHERE id <= 10`); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, func() bool { _, all := col.total(); return all >= n+10 }, "republished rows did not arrive")
	if d, _ := col.total(); d != n {
		t.Errorf("distinct messages = %d, want %d", d, n)
	}
	cancel()
	if err := <-relayDone; err != nil {
		t.Errorf("RunRelay() = %v", err)
	}
	if err := <-consumerDone; err != nil {
		t.Errorf("consumer Run() = %v", err)
	}
	var pending int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&pending); err != nil || pending != 0 {
		t.Errorf("pending outbox rows = %d, %v", pending, err)
	}
}

func produce(t *testing.T, client *kgo.Client, values ...string) {
	t.Helper()
	recs := make([]*kgo.Record, len(values))
	for i, v := range values {
		recs[i] = &kgo.Record{Topic: topics[0], Value: []byte(v)}
	}
	if err := client.ProduceSync(t.Context(), recs...).FirstErr(); err != nil {
		t.Fatal(err)
	}
}

func TestConsumer_RetriesFailingHandler(t *testing.T) {
	brokers := kafkatest.Start(t)
	producer := setupTopics(t, brokers)
	produce(t, producer, "flaky")

	var (
		mu    sync.Mutex
		calls int
	)
	handler := func(context.Context, []byte) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls < 3 {
			return fmt.Errorf("database unavailable")
		}
		return nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := runConsumer(ctx, t, brokers, "retry-test", handler, time.Second)
	eventually(t, 60*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return calls >= 3 }, "handler was not retried")
	cancel()
	if err := <-done; err != nil {
		t.Error(err)
	}
}

func TestConsumer_ShutdownCommitsOnlyFinishedWork(t *testing.T) {
	brokers := kafkatest.Start(t)
	producer := setupTopics(t, brokers)

	// First run: "done" finishes, then "stuck" blocks past the grace period.
	produce(t, producer, "done")
	finished := &collector{}
	ctx1, cancel1 := context.WithCancel(t.Context())
	done1 := runConsumer(ctx1, t, brokers, "shutdown-test", finished.handle, 50*time.Millisecond)
	eventually(t, 60*time.Second, func() bool { d, _ := finished.total(); return d == 1 }, "first message not handled")
	time.Sleep(500 * time.Millisecond) // let the commit land
	cancel1()
	if err := <-done1; err != nil {
		t.Error(err)
	}

	produce(t, producer, "stuck")
	started := make(chan struct{}, 1)
	blocking := func(ctx context.Context, _ []byte) error {
		started <- struct{}{}
		<-ctx.Done() // never finishes on its own
		return ctx.Err()
	}
	ctx2, cancel2 := context.WithCancel(t.Context())
	done2 := runConsumer(ctx2, t, brokers, "shutdown-test", blocking, 50*time.Millisecond)
	select {
	case <-started:
	case <-time.After(60 * time.Second):
		t.Fatal("stuck message not delivered")
	}
	cancel2()
	select {
	case err := <-done2:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("consumer did not stop within the grace period")
	}

	// Third run: only the unfinished message is redelivered.
	again := &collector{}
	ctx3, cancel3 := context.WithCancel(t.Context())
	defer cancel3()
	done3 := runConsumer(ctx3, t, brokers, "shutdown-test", again.handle, time.Second)
	eventually(t, 60*time.Second, func() bool { d, _ := again.total(); return d >= 1 }, "unfinished message was not redelivered")
	time.Sleep(time.Second)
	again.mu.Lock()
	if again.seen["stuck"] != 1 || again.seen["done"] != 0 {
		t.Errorf("redelivered %v, want only the unfinished message", again.seen)
	}
	again.mu.Unlock()
	cancel3()
	if err := <-done3; err != nil {
		t.Error(err)
	}
}
