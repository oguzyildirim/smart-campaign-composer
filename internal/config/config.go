// Package config loads and validates process configuration from environment
// variables. Every binary loads its configuration once at startup and fails
// fast with an aggregated error when a value is missing or invalid.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Defaults applied when the corresponding variable is unset or empty.
const (
	DefaultAPIAddr          = ":8080"
	DefaultWorkerHealthAddr = ":8081"
	DefaultMockProviderAddr = ":9090"
	DefaultShutdownTimeout  = 10 * time.Second
	DefaultReadinessTimeout = 2 * time.Second
	DefaultRequestTimeout   = 30 * time.Second
	DefaultSeedUserCount    = 5000
	DefaultSeedRandomSeed   = 20261022
	MaxSeedUserCount        = 100000

	DefaultAnthropicModel    = "claude-sonnet-5-5"
	DefaultLLMSegmentTimeout = 20 * time.Second
	DefaultLLMDraftsTimeout  = 60 * time.Second
	DefaultLLMMaxAttempts    = 2
	DefaultLLMSDKMaxRetries  = 2
	// DefaultLLMMockStreamDelay paces the mock's streamed chunks so the UI
	// shows drafts arriving; about one second for the example drafts.
	DefaultLLMMockStreamDelay = 40 * time.Millisecond

	DefaultDeliveryMaxAttempts  = 5
	DefaultProviderURL          = "http://mock-provider:9090"
	DefaultProviderTimeout      = 5 * time.Second
	DefaultRetryBaseDelay       = time.Second
	DefaultRetryMaxDelay        = 60 * time.Second
	DefaultRetryBackoffFactor   = 2
	DefaultDeliveryLease        = 60 * time.Second
	DefaultWorkerConcurrency    = 16
	DefaultRateLimitPerSecond   = 90
	DefaultRateLimitBurst       = 5
	DefaultKafkaTopicPartitions = 6
	DefaultOutboxPollInterval   = 200 * time.Millisecond
	// Live progress: one shared poll per campaign and interval, whatever
	// the number of viewers.
	DefaultProgressPollInterval     = time.Second
	DefaultProgressMaxViewers       = 20
	DefaultProgressThroughputWindow = 10 * time.Second
	minProgressPollInterval         = 100 * time.Millisecond
	DefaultOutboxBatchSize          = 500
	DefaultDeliveryScanInterval     = time.Second
	// MaxSendsPerSecond is the product limit per channel. Rate plus burst
	// may not exceed it: a token bucket admits at most rate + burst sends
	// in any one-second window.
	MaxSendsPerSecond = 100

	// Mock provider fault defaults: about 20% of first attempts fail
	// transiently, 1% of recipients are rejected permanently.
	DefaultMockLatencyMin        = 10 * time.Millisecond
	DefaultMockLatencyMax        = 80 * time.Millisecond
	DefaultMockRateLimitRate     = 0.06
	DefaultMockRetryAfter        = time.Second
	DefaultMockServerErrorRate   = 0.08
	DefaultMockPermanentRate     = 0.01
	DefaultMockLostResponseRate  = 0.03
	DefaultMockLostResponseDelay = 8 * time.Second

	maxShutdownTimeout = 60 * time.Second
	maxCheckTimeout    = 30 * time.Second
	maxRequestTimeout  = 5 * time.Minute
	maxLLMCallTimeout  = 3 * time.Minute
	maxLLMAttempts     = 3
	maxLLMSDKRetries   = 5
	maxMockStreamDelay = time.Second
	// llmRouteSlack is added to the LLM routes' timeout so the service's
	// own per-call timeouts always fire first and produce a clean error.
	llmRouteSlack = 5 * time.Second
)

// LLMProvider selects the language model backend.
type LLMProvider string

// Supported providers.
const (
	LLMProviderMock   LLMProvider = "mock"
	LLMProviderClaude LLMProvider = "claude"
)

// modelID accepts Anthropic model ids such as claude-sonnet-5-5.
var modelID = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// Getenv looks up an environment variable. os.Getenv satisfies it; tests
// pass a map-backed function instead of mutating the process environment.
type Getenv func(key string) string

// Config is the configuration shared by the API and the worker.
type Config struct {
	LogLevel         slog.Level
	DatabaseURL      Secret // contains credentials; never log the raw value
	KafkaBrokers     []string
	ShutdownTimeout  time.Duration
	ReadinessTimeout time.Duration
	// AnthropicAPIKey is optional. When empty, the API uses the
	// deterministic mock LLM.
	AnthropicAPIKey Secret

	API      APIConfig
	Worker   WorkerConfig
	LLM      LLMConfig
	Delivery DeliveryConfig
}

// DeliveryConfig configures message delivery. MaxAttempts is used by the
// API (stored on each delivery at approval); the rest by the worker.
type DeliveryConfig struct {
	MaxAttempts     int
	ProviderURL     string
	ProviderTimeout time.Duration
	// Retry backoff: RetryBaseDelay·RetryBackoffFactor^(attempt-1), capped
	// at RetryMaxDelay, with full jitter.
	RetryBaseDelay     time.Duration
	RetryMaxDelay      time.Duration
	RetryBackoffFactor int
	// Lease is how long a claimed delivery stays exclusive to one worker.
	Lease       time.Duration
	Concurrency int
	// RateLimitPerSecond and RateLimitBurst configure each channel's
	// shared token bucket; their sum is at most MaxSendsPerSecond.
	RateLimitPerSecond  int
	RateLimitBurst      int
	TopicPartitions     int
	OutboxPollInterval  time.Duration
	OutboxBatchSize     int
	MaintenanceInterval time.Duration
}

// LLMConfig configures AI generation.
type LLMConfig struct {
	// Provider is LLM_PROVIDER, defaulting to claude when an API key is
	// set and mock otherwise.
	Provider LLMProvider
	Model    string
	// SegmentTimeout and DraftsTimeout bound each model call.
	SegmentTimeout time.Duration
	DraftsTimeout  time.Duration
	// MaxAttempts is the total number of calls per request, including
	// repair calls after invalid output.
	MaxAttempts int
	// SDKMaxRetries is the Claude SDK's retry count for transient HTTP
	// errors within one call.
	SDKMaxRetries int
	// MockStreamDelay is the pause between the mock LLM's streamed chunks.
	MockStreamDelay time.Duration
}

// RouteTimeout is the request timeout for routes that call the LLM: long
// enough for every attempt of the slower operation, plus slack.
func (c LLMConfig) RouteTimeout() time.Duration {
	return time.Duration(c.MaxAttempts)*max(c.SegmentTimeout, c.DraftsTimeout) + llmRouteSlack
}

// APIConfig holds settings used only by cmd/api.
type APIConfig struct {
	Addr           string
	RequestTimeout time.Duration
	Progress       ProgressConfig
}

// ProgressConfig configures the live campaign dashboard.
type ProgressConfig struct {
	// PollInterval is the time between two reads of one campaign's
	// progress, shared by all of its viewers.
	PollInterval time.Duration
	// MaxViewers bounds the open progress streams per campaign.
	MaxViewers int
	// ThroughputWindow is the period the send rate is measured over.
	ThroughputWindow time.Duration
}

// WorkerConfig holds settings used only by cmd/worker.
type WorkerConfig struct {
	// HealthAddr serves the worker's liveness and readiness probes.
	HealthAddr string
}

// MockProviderConfig is the configuration of the mock messaging provider,
// which has no database or queue dependencies.
type MockProviderConfig struct {
	LogLevel        slog.Level
	Addr            string
	ShutdownTimeout time.Duration
	// Seed drives the simulated faults; 0 picks a random seed at start.
	Seed uint64
	// Faults; rates are probabilities in [0, 1].
	LatencyMin           time.Duration
	LatencyMax           time.Duration
	RateLimitRate        float64
	RetryAfter           time.Duration
	ServerErrorRate      float64
	PermanentFailureRate float64
	LostResponseRate     float64
	LostResponseDelay    time.Duration
	// MaxPerSecond is the provider's own per-channel limit; 0 disables it.
	MaxPerSecond int
}

// SeedConfig is the configuration of cmd/seed.
type SeedConfig struct {
	LogLevel    slog.Level
	DatabaseURL Secret // contains credentials; never log the raw value
	UserCount   int
	RandomSeed  uint64
}

// Load reads and validates the API/worker configuration. All problems are
// reported together. Error messages never include secret values.
func Load(getenv Getenv) (Config, error) {
	p := parser{getenv: getenv}

	cfg := Config{
		LogLevel:         p.logLevel("LOG_LEVEL"),
		DatabaseURL:      p.databaseURL("DATABASE_URL"),
		KafkaBrokers:     p.brokers("KAFKA_BROKERS"),
		ShutdownTimeout:  p.duration("SHUTDOWN_TIMEOUT", DefaultShutdownTimeout, maxShutdownTimeout),
		ReadinessTimeout: p.duration("READINESS_TIMEOUT", DefaultReadinessTimeout, maxCheckTimeout),
		AnthropicAPIKey:  Secret(strings.TrimSpace(getenv("ANTHROPIC_API_KEY"))),
		API: APIConfig{
			Addr:           p.addr("API_HTTP_ADDR", DefaultAPIAddr),
			RequestTimeout: p.duration("HTTP_REQUEST_TIMEOUT", DefaultRequestTimeout, maxRequestTimeout),
			Progress: ProgressConfig{
				PollInterval:     p.duration("PROGRESS_POLL_INTERVAL", DefaultProgressPollInterval, 10*time.Second),
				MaxViewers:       p.intRange("PROGRESS_MAX_VIEWERS", DefaultProgressMaxViewers, 1, 1000),
				ThroughputWindow: p.duration("PROGRESS_THROUGHPUT_WINDOW", DefaultProgressThroughputWindow, 5*time.Minute),
			},
		},
		Worker: WorkerConfig{
			HealthAddr: p.addr("WORKER_HEALTH_ADDR", DefaultWorkerHealthAddr),
		},
	}
	cfg.Delivery = DeliveryConfig{
		MaxAttempts:         p.intRange("DELIVERY_MAX_ATTEMPTS", DefaultDeliveryMaxAttempts, 1, 20),
		ProviderURL:         p.httpURL("PROVIDER_URL", DefaultProviderURL),
		ProviderTimeout:     p.duration("PROVIDER_TIMEOUT", DefaultProviderTimeout, time.Minute),
		RetryBaseDelay:      p.duration("RETRY_BASE_DELAY", DefaultRetryBaseDelay, time.Minute),
		RetryMaxDelay:       p.duration("RETRY_MAX_DELAY", DefaultRetryMaxDelay, time.Hour),
		RetryBackoffFactor:  p.intRange("RETRY_BACKOFF_FACTOR", DefaultRetryBackoffFactor, 1, 10),
		Lease:               p.duration("DELIVERY_LEASE", DefaultDeliveryLease, 10*time.Minute),
		Concurrency:         p.intRange("WORKER_CONCURRENCY", DefaultWorkerConcurrency, 1, 256),
		RateLimitPerSecond:  p.intRange("RATE_LIMIT_PER_SECOND", DefaultRateLimitPerSecond, 1, MaxSendsPerSecond),
		RateLimitBurst:      p.intRange("RATE_LIMIT_BURST", DefaultRateLimitBurst, 1, MaxSendsPerSecond),
		TopicPartitions:     p.intRange("KAFKA_TOPIC_PARTITIONS", DefaultKafkaTopicPartitions, 1, 64),
		OutboxPollInterval:  p.duration("OUTBOX_POLL_INTERVAL", DefaultOutboxPollInterval, 10*time.Second),
		OutboxBatchSize:     p.intRange("OUTBOX_BATCH_SIZE", DefaultOutboxBatchSize, 1, 5000),
		MaintenanceInterval: p.duration("DELIVERY_SCAN_INTERVAL", DefaultDeliveryScanInterval, time.Minute),
	}
	if cfg.API.Progress.PollInterval < minProgressPollInterval {
		p.fail("PROGRESS_POLL_INTERVAL", "must be at least %s, got %s", minProgressPollInterval, cfg.API.Progress.PollInterval)
	}
	d := cfg.Delivery
	if d.RateLimitPerSecond+d.RateLimitBurst > MaxSendsPerSecond {
		p.fail("RATE_LIMIT_PER_SECOND", "plus RATE_LIMIT_BURST must be at most %d, got %d", MaxSendsPerSecond, d.RateLimitPerSecond+d.RateLimitBurst)
	}
	if d.RetryMaxDelay < d.RetryBaseDelay {
		p.fail("RETRY_MAX_DELAY", "must be at least RETRY_BASE_DELAY (%s)", d.RetryBaseDelay)
	}
	if d.Lease < 2*d.ProviderTimeout {
		p.fail("DELIVERY_LEASE", "must be at least twice PROVIDER_TIMEOUT (%s)", d.ProviderTimeout)
	}
	cfg.LLM = LLMConfig{
		Provider:        p.llmProvider("LLM_PROVIDER", cfg.AnthropicAPIKey.IsSet()),
		Model:           p.model("ANTHROPIC_MODEL", DefaultAnthropicModel),
		SegmentTimeout:  p.duration("LLM_SEGMENT_TIMEOUT", DefaultLLMSegmentTimeout, maxLLMCallTimeout),
		DraftsTimeout:   p.duration("LLM_DRAFTS_TIMEOUT", DefaultLLMDraftsTimeout, maxLLMCallTimeout),
		MaxAttempts:     p.intRange("LLM_MAX_ATTEMPTS", DefaultLLMMaxAttempts, 1, maxLLMAttempts),
		SDKMaxRetries:   p.intRange("LLM_SDK_MAX_RETRIES", DefaultLLMSDKMaxRetries, 0, maxLLMSDKRetries),
		MockStreamDelay: p.duration("LLM_MOCK_STREAM_DELAY", DefaultLLMMockStreamDelay, maxMockStreamDelay),
	}

	if err := p.err(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// LoadMockProvider reads and validates the mock provider configuration.
func LoadMockProvider(getenv Getenv) (MockProviderConfig, error) {
	p := parser{getenv: getenv}

	cfg := MockProviderConfig{
		LogLevel:        p.logLevel("LOG_LEVEL"),
		Addr:            p.addr("MOCK_PROVIDER_ADDR", DefaultMockProviderAddr),
		ShutdownTimeout: p.duration("SHUTDOWN_TIMEOUT", DefaultShutdownTimeout, maxShutdownTimeout),
		Seed:            p.uint64("MOCK_SEED", 0),

		LatencyMin:           p.durationOrZero("MOCK_LATENCY_MIN", DefaultMockLatencyMin, 10*time.Second),
		LatencyMax:           p.durationOrZero("MOCK_LATENCY_MAX", DefaultMockLatencyMax, 10*time.Second),
		RateLimitRate:        p.probability("MOCK_RATE_LIMIT_RATE", DefaultMockRateLimitRate),
		RetryAfter:           p.duration("MOCK_RETRY_AFTER", DefaultMockRetryAfter, 5*time.Minute),
		ServerErrorRate:      p.probability("MOCK_SERVER_ERROR_RATE", DefaultMockServerErrorRate),
		PermanentFailureRate: p.probability("MOCK_PERMANENT_FAILURE_RATE", DefaultMockPermanentRate),
		LostResponseRate:     p.probability("MOCK_LOST_RESPONSE_RATE", DefaultMockLostResponseRate),
		LostResponseDelay:    p.duration("MOCK_LOST_RESPONSE_DELAY", DefaultMockLostResponseDelay, 5*time.Minute),
		MaxPerSecond:         p.intRange("MOCK_MAX_PER_SECOND", MaxSendsPerSecond, 0, 100000),
	}
	if cfg.LatencyMax < cfg.LatencyMin {
		p.fail("MOCK_LATENCY_MAX", "must be at least MOCK_LATENCY_MIN (%s)", cfg.LatencyMin)
	}

	if err := p.err(); err != nil {
		return MockProviderConfig{}, err
	}
	return cfg, nil
}

// LoadSeed reads and validates the seed command configuration.
func LoadSeed(getenv Getenv) (SeedConfig, error) {
	p := parser{getenv: getenv}

	cfg := SeedConfig{
		LogLevel:    p.logLevel("LOG_LEVEL"),
		DatabaseURL: p.databaseURL("DATABASE_URL"),
		UserCount:   p.intRange("SEED_USER_COUNT", DefaultSeedUserCount, 1, MaxSeedUserCount),
		RandomSeed:  p.uint64("SEED_RANDOM_SEED", DefaultSeedRandomSeed),
	}

	if err := p.err(); err != nil {
		return SeedConfig{}, err
	}
	return cfg, nil
}

// parser accumulates validation errors so callers see every problem at once.
type parser struct {
	getenv Getenv
	errs   []error
}

func (p *parser) get(key string) string { return strings.TrimSpace(p.getenv(key)) }

func (p *parser) fail(key, format string, args ...any) {
	p.errs = append(p.errs, fmt.Errorf("%s: "+format, append([]any{key}, args...)...))
}

func (p *parser) err() error {
	if len(p.errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration: %w", errors.Join(p.errs...))
}

func (p *parser) logLevel(key string) slog.Level {
	raw := p.get(key)
	if raw == "" {
		return slog.LevelInfo
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		p.fail(key, "must be one of debug, info, warn, error")
		return slog.LevelInfo
	}
	return level
}

func (p *parser) databaseURL(key string) Secret {
	raw := p.get(key)
	if raw == "" {
		p.fail(key, "is required")
		return ""
	}
	// The value carries credentials, so errors describe the problem without
	// echoing the value or the underlying parse error (which may quote it).
	u, err := url.Parse(raw)
	if err != nil {
		p.fail(key, "is not a valid URL")
		return ""
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		p.fail(key, "must use the postgres:// or postgresql:// scheme")
		return ""
	}
	if u.Host == "" {
		p.fail(key, "must include a host")
		return ""
	}
	return Secret(raw)
}

func (p *parser) brokers(key string) []string {
	raw := p.get(key)
	if raw == "" {
		p.fail(key, "is required")
		return nil
	}
	var brokers []string
	for b := range strings.SplitSeq(raw, ",") {
		b = strings.TrimSpace(b)
		host, port, err := net.SplitHostPort(b)
		if err != nil || host == "" || !validPort(port) {
			p.fail(key, "entry %q must be host:port", b)
			continue
		}
		brokers = append(brokers, b)
	}
	return brokers
}

func (p *parser) addr(key, def string) string {
	raw := p.get(key)
	if raw == "" {
		return def
	}
	if _, port, err := net.SplitHostPort(raw); err != nil || !validPort(port) {
		p.fail(key, "must be [host]:port, got %q", raw)
		return def
	}
	return raw
}

func (p *parser) duration(key string, def, upper time.Duration) time.Duration {
	raw := p.get(key)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		p.fail(key, "must be a duration such as 5s, got %q", raw)
		return def
	}
	if d <= 0 || d > upper {
		p.fail(key, "must be greater than 0 and at most %s, got %s", upper, d)
		return def
	}
	return d
}

// durationOrZero is duration that also accepts 0.
func (p *parser) durationOrZero(key string, def, upper time.Duration) time.Duration {
	if raw := p.get(key); raw == "0" || raw == "0s" {
		return 0
	}
	return p.duration(key, def, upper)
}

func (p *parser) probability(key string, def float64) float64 {
	raw := p.get(key)
	if raw == "" {
		return def
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || f < 0 || f > 1 {
		p.fail(key, "must be a number between 0 and 1, got %q", raw)
		return def
	}
	return f
}

// httpURL accepts an http(s) URL with a host and no credentials.
func (p *parser) httpURL(key, def string) string {
	raw := p.get(key)
	if raw == "" {
		return def
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		p.fail(key, "must be an http(s)://host[:port] URL without credentials")
		return def
	}
	return raw
}

func (p *parser) intRange(key string, def, lower, upper int) int {
	raw := p.get(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lower || n > upper {
		p.fail(key, "must be an integer between %d and %d, got %q", lower, upper, raw)
		return def
	}
	return n
}

func (p *parser) uint64(key string, def uint64) uint64 {
	raw := p.get(key)
	if raw == "" {
		return def
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		p.fail(key, "must be a non-negative integer, got %q", raw)
		return def
	}
	return n
}

// llmProvider defaults to claude when an API key is configured. Choosing
// claude explicitly without a key is an error rather than a silent mock.
func (p *parser) llmProvider(key string, haveKey bool) LLMProvider {
	switch raw := LLMProvider(p.get(key)); raw {
	case "":
		if haveKey {
			return LLMProviderClaude
		}
		return LLMProviderMock
	case LLMProviderMock:
		return raw
	case LLMProviderClaude:
		if !haveKey {
			p.fail(key, "claude requires ANTHROPIC_API_KEY")
		}
		return raw
	default:
		p.fail(key, "must be one of mock, claude")
		return LLMProviderMock
	}
}

func (p *parser) model(key, def string) string {
	raw := p.get(key)
	if raw == "" {
		return def
	}
	if !modelID.MatchString(raw) {
		p.fail(key, "must be a model id such as %s", def)
		return def
	}
	return raw
}

func validPort(port string) bool {
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535
}
