// Command mock-provider simulates an SMS/email delivery provider for local
// development and tests. It never contacts a real messaging service; see
// internal/mockprovider for the API, the fake-recipient guard, and the
// injected faults.
//
// Usage:
//
//	mock-provider                    run the server
//	mock-provider healthcheck URL    exit 0 if URL returns 200
package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/oguzyildirim/smart-campaign-composer/internal/config"
	"github.com/oguzyildirim/smart-campaign-composer/internal/health"
	"github.com/oguzyildirim/smart-campaign-composer/internal/mockprovider"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/httpserver"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/logging"
)

const service = "mock-provider"

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
		logging.New(os.Stderr, service, slog.LevelError).Error("mock provider exited with error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadMockProvider(os.Getenv)
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, service, cfg.LogLevel)

	seed := cfg.Seed
	if seed == 0 {
		var b [8]byte
		_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
		seed = binary.BigEndian.Uint64(b[:])
	}
	faults := mockprovider.Config{
		Seed:                 seed,
		LatencyMin:           cfg.LatencyMin,
		LatencyMax:           cfg.LatencyMax,
		RateLimitRate:        cfg.RateLimitRate,
		RetryAfter:           cfg.RetryAfter,
		ServerErrorRate:      cfg.ServerErrorRate,
		PermanentFailureRate: cfg.PermanentFailureRate,
		LostResponseRate:     cfg.LostResponseRate,
		LostResponseDelay:    cfg.LostResponseDelay,
		MaxPerSecond:         cfg.MaxPerSecond,
	}
	logger.Info("starting", "addr", cfg.Addr, "seed", seed,
		"rate_limit_rate", faults.RateLimitRate, "server_error_rate", faults.ServerErrorRate,
		"permanent_failure_rate", faults.PermanentFailureRate, "lost_response_rate", faults.LostResponseRate,
		"max_per_second", faults.MaxPerSecond)

	// No router-wide timeout: a "lost response" deliberately stalls longer
	// than clients wait.
	router := httpserver.NewRouter(logger, 0)
	router.Get("/healthz", health.LivenessHandler())
	router.Handle("/v1/*", mockprovider.New(faults, logger).Handler())

	srv := httpserver.New(cfg.Addr, router, logger)
	return httpserver.Run(ctx, srv, cfg.ShutdownTimeout, logger)
}
