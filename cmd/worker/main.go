// Command worker runs background campaign processing.
//
// Usage:
//
//	worker                    run the worker
//	worker healthcheck URL    exit 0 if URL returns 200 (container healthcheck)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sync/errgroup"

	"github.com/oguzyildirim/smart-campaign-composer/internal/config"
	"github.com/oguzyildirim/smart-campaign-composer/internal/health"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/httpserver"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/kafka"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/logging"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/postgres"
	"github.com/oguzyildirim/smart-campaign-composer/internal/worker"
)

const service = "worker"

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
	logger := logging.New(os.Stdout, service, cfg.LogLevel)
	logger.Info("starting", "health_addr", cfg.Worker.HealthAddr, "kafka_brokers", cfg.KafkaBrokers)

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL.Reveal())
	if err != nil {
		return err
	}
	defer pool.Close()

	kafkaClient, err := kafka.NewClient(cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer kafkaClient.Close()

	readiness := health.NewReadiness(map[string]health.Checker{
		"postgres": postgres.NewChecker(pool),
		"kafka":    kafka.NewChecker(kafkaClient),
	}, cfg.ReadinessTimeout, logger)

	// The worker serves only probes; it has no public API.
	router := httpserver.NewRouter(logger, cfg.ReadinessTimeout*2)
	router.Get("/healthz", health.LivenessHandler())
	router.Get("/readyz", readiness.ReadinessHandler())
	srv := httpserver.New(cfg.Worker.HealthAddr, router, logger)

	// If either the worker loop or the probe server fails, cancel the other
	// so the process exits instead of running half-alive.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpserver.Run(gctx, srv, cfg.ShutdownTimeout, logger) })
	g.Go(func() error {
		if err := worker.New(readiness, logger).Run(gctx); err != nil {
			return fmt.Errorf("worker loop: %w", err)
		}
		if ctx.Err() == nil {
			return errors.New("worker loop exited unexpectedly")
		}
		return nil
	})
	return g.Wait()
}
