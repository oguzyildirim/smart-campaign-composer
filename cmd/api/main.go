// Command api serves the Smart Campaign Composer HTTP API.
//
// Usage:
//
//	api                    run the server
//	api healthcheck URL    exit 0 if URL returns 200 (container healthcheck)
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/config"
	"github.com/oguzyildirim/smart-campaign-composer/internal/health"
	"github.com/oguzyildirim/smart-campaign-composer/internal/httpapi"
	"github.com/oguzyildirim/smart-campaign-composer/internal/llm"
	"github.com/oguzyildirim/smart-campaign-composer/internal/llm/mockllm"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/claude"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/httpserver"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/kafka"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/logging"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/postgres"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

const service = "api"

const (
	// previewSamples is the number of recipients a preview renders per
	// channel.
	previewSamples = 3
	// sseHeartbeat keeps idle proxies from closing a draft stream.
	sseHeartbeat = 15 * time.Second
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
		logging.New(os.Stderr, service, slog.LevelError).Error("api exited with error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, service, cfg.LogLevel)
	logger.Info("starting",
		"addr", cfg.API.Addr,
		"llm_provider", cfg.LLM.Provider,
		"llm_model", cfg.LLM.Model,
		"kafka_brokers", cfg.KafkaBrokers,
	)

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

	audience := postgres.NewAudienceRepository(pool)
	segments := segment.NewService(audience, time.Now)
	generator := campaign.NewGenerator(newLLMClient(cfg, logger), segments, campaign.GeneratorConfig{
		SegmentTimeout: cfg.LLM.SegmentTimeout,
		DraftsTimeout:  cfg.LLM.DraftsTimeout,
		MaxAttempts:    cfg.LLM.MaxAttempts,
	}, logger)
	campaigns := campaign.NewService(generator, postgres.NewCampaignStore(pool), audience, campaign.ServiceConfig{
		GenerationTimeout: cfg.LLM.RouteTimeout(),
		PreviewSamples:    previewSamples,
	}, time.Now, logger)

	// Timeouts are per route: LLM routes need room for every attempt, and
	// the draft stream is bounded by the service's generation timeout.
	router := httpserver.NewRouter(logger, 0)
	defaultTimeout := middleware.Timeout(cfg.API.RequestTimeout)
	router.With(defaultTimeout).Get("/healthz", health.LivenessHandler())
	router.With(defaultTimeout).Get("/readyz", readiness.ReadinessHandler())
	router.Route("/api/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(defaultTimeout)
			httpapi.MountSegments(r, segments, logger)
		})
		httpapi.MountCampaigns(r, httpapi.CampaignRoutes{
			Service:        campaigns,
			Logger:         logger,
			RequestTimeout: cfg.API.RequestTimeout,
			CreateTimeout:  cfg.LLM.RouteTimeout(),
			Heartbeat:      sseHeartbeat,
		})
	})

	srv := httpserver.New(cfg.API.Addr, router, logger)
	return httpserver.Run(ctx, srv, cfg.ShutdownTimeout, logger)
}

// newLLMClient returns the configured model client. The API key is
// revealed only here, at the point of use.
func newLLMClient(cfg config.Config, logger *slog.Logger) llm.Client {
	if cfg.LLM.Provider == config.LLMProviderClaude {
		return claude.New(claude.Config{
			APIKey:     cfg.AnthropicAPIKey.Reveal(),
			Model:      cfg.LLM.Model,
			MaxRetries: cfg.LLM.SDKMaxRetries,
		}, logger)
	}
	return mockllm.New().WithStreamDelay(cfg.LLM.MockStreamDelay)
}
