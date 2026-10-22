// Command seed loads deterministic fake users, consents, cart events, and
// orders into the database. It is idempotent: when users already exist it
// logs and exits without writing. Compose runs it once after migrations.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/config"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/logging"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/postgres"
	"github.com/oguzyildirim/smart-campaign-composer/internal/seed"
)

const service = "seed"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx)
	stop()
	if err != nil {
		logging.New(os.Stderr, service, slog.LevelError).Error("seed failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadSeed(os.Getenv)
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, service, cfg.LogLevel)

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL.Reveal())
	if err != nil {
		return err
	}
	defer pool.Close()

	start := time.Now()
	ds := seed.Generate(seed.Options{UserCount: cfg.UserCount, RandomSeed: cfg.RandomSeed, Now: start})
	res, err := seed.Load(ctx, pool, ds)
	if err != nil {
		return err
	}
	if !res.Inserted {
		logger.Info("database already seeded; nothing to do")
		return nil
	}
	logger.Info("seed complete",
		"users", res.Users,
		"consents", res.Consents,
		"cart_events", res.CartEvents,
		"orders", res.Orders,
		"random_seed", cfg.RandomSeed,
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return nil
}
