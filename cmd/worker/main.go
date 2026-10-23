// Command worker delivers approved campaign messages: it relays the
// outbox to Kafka, consumes delivery jobs, sends them through the mock
// provider under the shared rate limit, and runs retry scheduling, lease
// recovery, and campaign completion.
//
// Usage:
//
//	worker                    run the worker
//	worker healthcheck URL    exit 0 if URL returns 200 (container healthcheck)
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/oguzyildirim/smart-campaign-composer/internal/config"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/delivery"
	"github.com/oguzyildirim/smart-campaign-composer/internal/health"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/httpserver"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/kafka"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/logging"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/postgres"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/provider"
	"github.com/oguzyildirim/smart-campaign-composer/internal/worker"
)

const (
	service       = "worker"
	consumerGroup = "delivery-workers"
	// maxPollRecords bounds one consumed batch; offsets are committed per
	// batch, so smaller batches redeliver less after a crash.
	maxPollRecords = 64
	// outboxRetention keeps published outbox rows for inspection.
	outboxRetention = 24 * time.Hour
)

func main() {
	if len(os.Args) == 3 && os.Args[1] == "healthcheck" {
		if err := httpserver.Probe(context.Background(), os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx)
	stop()
	if err != nil {
		logging.New(os.Stderr, service, slog.LevelError).Error("worker exited with error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	d := cfg.Delivery
	logger := logging.New(os.Stdout, service, cfg.LogLevel)
	logger.Info("starting", "health_addr", cfg.Worker.HealthAddr, "kafka_brokers", cfg.KafkaBrokers,
		"provider_url", d.ProviderURL, "rate_limit_per_second", d.RateLimitPerSecond, "rate_limit_burst", d.RateLimitBurst,
		"concurrency", d.Concurrency)

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL.Reveal())
	if err != nil {
		return err
	}
	defer pool.Close()

	producer, err := kafka.NewClient(cfg.KafkaBrokers, kafka.ProducerOpts()...)
	if err != nil {
		return err
	}
	defer producer.Close()

	topics := []string{delivery.Topic(content.ChannelSMS), delivery.Topic(content.ChannelEmail)}
	store := postgres.NewDeliveryStore(pool)
	sender, err := provider.New(d.ProviderURL, d.ProviderTimeout)
	if err != nil {
		return err
	}
	processor := delivery.NewProcessor(store,
		delivery.NewLimiter(postgres.NewTokenBucket(pool, d.RateLimitPerSecond, d.RateLimitBurst)),
		sender,
		delivery.ProcessorConfig{
			Lease:  d.Lease,
			Policy: delivery.RetryPolicy{BaseDelay: d.RetryBaseDelay, Factor: d.RetryBackoffFactor, MaxDelay: d.RetryMaxDelay},
		}, logger)
	consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers:        cfg.KafkaBrokers,
		Group:          consumerGroup,
		Topics:         topics,
		Concurrency:    d.Concurrency,
		MaxPollRecords: maxPollRecords,
		// Half the shutdown budget for in-flight sends; the rest covers
		// commits and closing.
		Grace: cfg.ShutdownTimeout / 2,
	}, processor.Handle, logger)
	if err != nil {
		return err
	}
	defer consumer.Close()

	readiness := health.NewReadiness(map[string]health.Checker{
		"postgres": postgres.NewChecker(pool),
		"kafka":    kafka.NewChecker(producer),
	}, cfg.ReadinessTimeout, logger)

	// The worker serves only probes; it has no public API.
	router := httpserver.NewRouter(logger, cfg.ReadinessTimeout*2)
	router.Get("/healthz", health.LivenessHandler())
	router.Get("/readyz", readiness.ReadinessHandler())
	srv := httpserver.New(cfg.Worker.HealthAddr, router, logger)

	w := worker.New(readiness, logger, worker.Config{
		Setup: func(ctx context.Context) error {
			return kafka.EnsureTopics(ctx, producer, d.TopicPartitions, topics...)
		},
		Tasks: []worker.Task{
			{Name: "outbox relay", Run: func(ctx context.Context) error {
				return delivery.RunRelay(ctx, store, kafka.NewPublisher(producer),
					delivery.RelayConfig{BatchSize: d.OutboxBatchSize, PollInterval: d.OutboxPollInterval}, logger)
			}},
			{Name: "delivery consumer", Run: consumer.Run},
			{Name: "delivery maintenance", Run: func(ctx context.Context) error {
				return delivery.RunMaintenance(ctx, store, delivery.MaintenanceConfig{
					Interval: d.MaintenanceInterval, BatchSize: d.OutboxBatchSize, OutboxRetention: outboxRetention,
				}, logger)
			}},
		},
	})

	// If either the worker or the probe server fails, cancel the other so
	// the process exits instead of running half-alive.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpserver.Run(gctx, srv, cfg.ShutdownTimeout, logger) })
	g.Go(func() error { return w.Run(gctx) })
	return g.Wait()
}
