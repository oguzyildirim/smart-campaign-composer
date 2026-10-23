package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"golang.org/x/sync/errgroup"

	"github.com/oguzyildirim/smart-campaign-composer/internal/delivery"
)

// EnsureTopics creates topics with the given partition count and the
// broker's default replication factor. Existing topics are left as they
// are.
func EnsureTopics(ctx context.Context, client *kgo.Client, partitions int, topics ...string) error {
	if partitions < 1 || partitions > math.MaxInt32 {
		return fmt.Errorf("create topics: invalid partition count %d", partitions)
	}
	resp, err := kadm.NewClient(client).CreateTopics(ctx, int32(partitions), -1, nil, topics...)
	if err != nil {
		return fmt.Errorf("create topics: %w", err)
	}
	for _, r := range resp {
		if r.Err != nil && !errors.Is(r.Err, kerr.TopicAlreadyExists) {
			return fmt.Errorf("create topic %s: %w", r.Topic, r.Err)
		}
	}
	return nil
}

// ProducerOpts are the client options for publishing delivery jobs:
// acknowledged by every in-sync replica, idempotent (franz-go's default,
// stated here), and bounded in time so a relay never hangs on a dead
// broker.
func ProducerOpts() []kgo.Opt {
	return []kgo.Opt{
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RecordDeliveryTimeout(30 * time.Second),
	}
}

// Publisher implements delivery.Publisher.
type Publisher struct {
	client *kgo.Client
}

var _ delivery.Publisher = (*Publisher)(nil)

// NewPublisher returns a publisher on client, which should be created with
// ProducerOpts.
func NewPublisher(client *kgo.Client) *Publisher { return &Publisher{client: client} }

// Publish implements delivery.Publisher. It returns once every message is
// acknowledged, or with the first failure.
func (p *Publisher) Publish(ctx context.Context, msgs []delivery.OutboxMessage) error {
	recs := make([]*kgo.Record, len(msgs))
	for i, m := range msgs {
		recs[i] = &kgo.Record{Topic: m.Topic, Key: []byte(m.Key), Value: m.Payload}
	}
	if err := p.client.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		return fmt.Errorf("produce %d messages: %w", len(msgs), err)
	}
	return nil
}

// Handler processes one message value. An error means "retry this
// message"; the consumer retries it until it succeeds or work stops.
type Handler func(ctx context.Context, value []byte) error

// ConsumerConfig configures a Consumer.
type ConsumerConfig struct {
	Brokers []string
	Group   string
	Topics  []string
	// Concurrency is the number of messages processed at once.
	Concurrency int
	// MaxPollRecords bounds one batch; offsets are committed per batch.
	MaxPollRecords int
	// Grace is how long in-flight messages may finish after shutdown
	// starts. Unfinished messages are not committed and are redelivered.
	Grace time.Duration
}

// Consumer reads delivery jobs in a consumer group. Offsets are committed
// only after every message of a polled batch was handled, and rebalances
// wait until then, so a committed offset never covers unfinished work.
type Consumer struct {
	client  *kgo.Client
	cfg     ConsumerConfig
	handler Handler
	logger  *slog.Logger
}

// Retry pacing for a failing handler (for example, the database is down).
const (
	handlerRetryMin = 100 * time.Millisecond
	handlerRetryMax = 5 * time.Second
	commitTimeout   = 5 * time.Second
	fetchErrorPause = time.Second
)

// NewConsumer returns a consumer; call Run, then Close.
func NewConsumer(cfg ConsumerConfig, handler Handler, logger *slog.Logger) (*Consumer, error) {
	client, err := NewClient(cfg.Brokers,
		kgo.ConsumerGroup(cfg.Group),
		kgo.ConsumeTopics(cfg.Topics...),
		// Start from the beginning when the group has no committed offset,
		// so jobs published before the first worker joined are not skipped.
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
	)
	if err != nil {
		return nil, err
	}
	return &Consumer{client: client, cfg: cfg, handler: handler, logger: logger}, nil
}

// Client returns the underlying client, for readiness checks.
func (c *Consumer) Client() *kgo.Client { return c.client }

// Close leaves the group and closes the client.
func (c *Consumer) Close() { c.client.Close() }

// Run polls and handles batches until ctx ends. On shutdown it stops
// polling, gives in-flight messages Grace to finish, and commits only if
// the whole batch finished.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		fetches := c.client.PollRecords(ctx, c.cfg.MaxPollRecords)
		if fetches.IsClientClosed() || ctx.Err() != nil {
			c.client.AllowRebalance()
			return nil
		}
		fetchFailed := false
		fetches.EachError(func(topic string, partition int32, err error) {
			fetchFailed = true
			c.logger.WarnContext(ctx, "kafka fetch error", "topic", topic, "partition", partition, "error", err)
		})
		recs := fetches.Records()
		if len(recs) == 0 {
			c.client.AllowRebalance()
			if fetchFailed && !sleepCtx(ctx, fetchErrorPause) {
				return nil
			}
			continue
		}

		finished := c.handleBatch(ctx, recs)
		if finished {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commitTimeout)
			if err := c.client.CommitRecords(cctx, recs...); err != nil {
				// The batch is redelivered; claims make that harmless.
				c.logger.WarnContext(ctx, "kafka commit failed", "records", len(recs), "error", err)
			}
			cancel()
		}
		c.client.AllowRebalance()
		if !finished {
			c.logger.InfoContext(ctx, "shutdown left messages unfinished; they will be redelivered")
			return nil
		}
	}
}

// handleBatch handles every record concurrently and reports whether all of
// them finished. Work continues for Grace after ctx ends.
func (c *Consumer) handleBatch(ctx context.Context, recs []*kgo.Record) bool {
	work, cancelWork := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelWork()
	stop := context.AfterFunc(ctx, func() {
		timer := time.AfterFunc(c.cfg.Grace, cancelWork)
		context.AfterFunc(work, func() { timer.Stop() })
	})
	defer stop()

	var g errgroup.Group
	g.SetLimit(max(c.cfg.Concurrency, 1))
	for _, rec := range recs {
		g.Go(func() error { return c.handle(work, rec) })
	}
	return g.Wait() == nil
}

// handle retries the handler with capped backoff until it succeeds or
// work ends, which is returned as an error.
func (c *Consumer) handle(work context.Context, rec *kgo.Record) error {
	backoff := handlerRetryMin
	for {
		err := c.handler(work, rec.Value)
		if err == nil {
			return nil
		}
		if work.Err() != nil {
			return work.Err()
		}
		c.logger.WarnContext(work, "message handler failed; retrying",
			"topic", rec.Topic, "partition", rec.Partition, "offset", rec.Offset, "error", err, "retry_in", backoff.String())
		if !sleepCtx(work, backoff) {
			return work.Err()
		}
		backoff = min(backoff*2, handlerRetryMax)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
