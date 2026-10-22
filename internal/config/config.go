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

	maxShutdownTimeout = 60 * time.Second
	maxCheckTimeout    = 30 * time.Second
	maxRequestTimeout  = 5 * time.Minute
)

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
	// AnthropicAPIKey is optional. When empty, later milestones use the
	// deterministic mock LLM.
	AnthropicAPIKey Secret

	API    APIConfig
	Worker WorkerConfig
}

// APIConfig holds settings used only by cmd/api.
type APIConfig struct {
	Addr           string
	RequestTimeout time.Duration
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
		},
		Worker: WorkerConfig{
			HealthAddr: p.addr("WORKER_HEALTH_ADDR", DefaultWorkerHealthAddr),
		},
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

func validPort(port string) bool {
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535
}
