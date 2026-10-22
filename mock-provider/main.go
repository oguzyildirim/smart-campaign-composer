// Command mock-provider simulates an SMS/email delivery provider for local
// development and tests. It never contacts a real messaging service.
//
// This milestone exposes only GET /healthz; the send endpoints with
// simulated failures, latency, and 429 responses arrive with delivery.
//
// Usage:
//
//	mock-provider                    run the server
//	mock-provider healthcheck URL    exit 0 if URL returns 200
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/config"
	"github.com/oguzyildirim/smart-campaign-composer/internal/health"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/httpserver"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/logging"
)

const (
	service        = "mock-provider"
	requestTimeout = 10 * time.Second
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

	router := httpserver.NewRouter(logger, requestTimeout)
	router.Get("/healthz", health.LivenessHandler())

	srv := httpserver.New(cfg.Addr, router, logger)
	return httpserver.Run(ctx, srv, cfg.ShutdownTimeout, logger)
}
