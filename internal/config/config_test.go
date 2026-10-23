package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"testing"
	"time"
)

const testDatabaseURL = "postgres://app:s3cr3t-pw@localhost:5432/campaigns?sslmode=disable"

func envFrom(m map[string]string) Getenv {
	return func(key string) string { return m[key] }
}

func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":  testDatabaseURL,
		"KAFKA_BROKERS": "kafka:9092",
	}
}

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load(envFrom(validEnv()))
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	if cfg.API.Addr != DefaultAPIAddr {
		t.Errorf("API.Addr = %q, want %q", cfg.API.Addr, DefaultAPIAddr)
	}
	if cfg.Worker.HealthAddr != DefaultWorkerHealthAddr {
		t.Errorf("Worker.HealthAddr = %q, want %q", cfg.Worker.HealthAddr, DefaultWorkerHealthAddr)
	}
	if cfg.ShutdownTimeout != DefaultShutdownTimeout {
		t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, DefaultShutdownTimeout)
	}
	if cfg.ReadinessTimeout != DefaultReadinessTimeout {
		t.Errorf("ReadinessTimeout = %v, want %v", cfg.ReadinessTimeout, DefaultReadinessTimeout)
	}
	if cfg.API.RequestTimeout != DefaultRequestTimeout {
		t.Errorf("API.RequestTimeout = %v, want %v", cfg.API.RequestTimeout, DefaultRequestTimeout)
	}
	if cfg.DatabaseURL.Reveal() != testDatabaseURL {
		t.Errorf("DatabaseURL not preserved")
	}
	if len(cfg.KafkaBrokers) != 1 || cfg.KafkaBrokers[0] != "kafka:9092" {
		t.Errorf("KafkaBrokers = %v, want [kafka:9092]", cfg.KafkaBrokers)
	}
	if cfg.AnthropicAPIKey.IsSet() {
		t.Errorf("AnthropicAPIKey should be optional and empty by default")
	}
}

func TestLoad_Overrides(t *testing.T) {
	env := validEnv()
	env["LOG_LEVEL"] = "debug"
	env["KAFKA_BROKERS"] = " kafka-1:9092 , kafka-2:9093 "
	env["API_HTTP_ADDR"] = "127.0.0.1:9000"
	env["WORKER_HEALTH_ADDR"] = ":9001"
	env["SHUTDOWN_TIMEOUT"] = "3s"
	env["READINESS_TIMEOUT"] = "500ms"
	env["HTTP_REQUEST_TIMEOUT"] = "1m"
	env["ANTHROPIC_API_KEY"] = "sk-test-placeholder"

	cfg, err := Load(envFrom(env))
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want debug", cfg.LogLevel)
	}
	if got := strings.Join(cfg.KafkaBrokers, ","); got != "kafka-1:9092,kafka-2:9093" {
		t.Errorf("KafkaBrokers = %q", got)
	}
	if cfg.API.Addr != "127.0.0.1:9000" || cfg.Worker.HealthAddr != ":9001" {
		t.Errorf("addresses = %q, %q", cfg.API.Addr, cfg.Worker.HealthAddr)
	}
	if cfg.ShutdownTimeout != 3*time.Second || cfg.ReadinessTimeout != 500*time.Millisecond ||
		cfg.API.RequestTimeout != time.Minute {
		t.Errorf("timeouts = %v, %v, %v", cfg.ShutdownTimeout, cfg.ReadinessTimeout, cfg.API.RequestTimeout)
	}
	if cfg.AnthropicAPIKey.Reveal() != "sk-test-placeholder" {
		t.Errorf("AnthropicAPIKey not loaded")
	}
}

func TestLoad_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{"missing database url", "DATABASE_URL", "", "DATABASE_URL: is required"},
		{"whitespace database url", "DATABASE_URL", "   ", "DATABASE_URL: is required"},
		{"wrong database scheme", "DATABASE_URL", "mysql://u:p@db/x", "DATABASE_URL: must use the postgres"},
		{"database url without host", "DATABASE_URL", "postgres:///campaigns", "DATABASE_URL: must include a host"},
		{"malformed database url", "DATABASE_URL", "postgres://u:p@%zz/db", "DATABASE_URL: is not a valid URL"},
		{"missing brokers", "KAFKA_BROKERS", "", "KAFKA_BROKERS: is required"},
		{"broker without port", "KAFKA_BROKERS", "kafka", `KAFKA_BROKERS: entry "kafka" must be host:port`},
		{"broker with bad port", "KAFKA_BROKERS", "kafka:99999", "must be host:port"},
		{"empty broker entry", "KAFKA_BROKERS", "kafka:9092,", `entry "" must be host:port`},
		{"unknown log level", "LOG_LEVEL", "verbose", "LOG_LEVEL: must be one of"},
		{"bad api addr", "API_HTTP_ADDR", "8080", "API_HTTP_ADDR: must be [host]:port"},
		{"bad worker addr port", "WORKER_HEALTH_ADDR", ":0", "WORKER_HEALTH_ADDR: must be [host]:port"},
		{"unparsable duration", "SHUTDOWN_TIMEOUT", "ten", "SHUTDOWN_TIMEOUT: must be a duration"},
		{"zero duration", "SHUTDOWN_TIMEOUT", "0s", "SHUTDOWN_TIMEOUT: must be greater than 0"},
		{"negative duration", "READINESS_TIMEOUT", "-1s", "READINESS_TIMEOUT: must be greater than 0"},
		{"duration above max", "SHUTDOWN_TIMEOUT", "2m", "at most 1m0s"},
		{"request timeout above max", "HTTP_REQUEST_TIMEOUT", "1h", "HTTP_REQUEST_TIMEOUT: must be greater than 0"},
		{"unknown llm provider", "LLM_PROVIDER", "openai", "LLM_PROVIDER: must be one of mock, claude"},
		{"claude without key", "LLM_PROVIDER", "claude", "LLM_PROVIDER: claude requires ANTHROPIC_API_KEY"},
		{"model with spaces", "ANTHROPIC_MODEL", "claude sonnet", "ANTHROPIC_MODEL: must be a model id"},
		{"model with path", "ANTHROPIC_MODEL", "../claude", "ANTHROPIC_MODEL: must be a model id"},
		{"segment timeout above max", "LLM_SEGMENT_TIMEOUT", "10m", "LLM_SEGMENT_TIMEOUT: must be greater than 0"},
		{"drafts timeout zero", "LLM_DRAFTS_TIMEOUT", "0s", "LLM_DRAFTS_TIMEOUT: must be greater than 0"},
		{"zero attempts", "LLM_MAX_ATTEMPTS", "0", "LLM_MAX_ATTEMPTS: must be an integer between 1 and 3"},
		{"too many attempts", "LLM_MAX_ATTEMPTS", "4", "LLM_MAX_ATTEMPTS: must be an integer between 1 and 3"},
		{"negative sdk retries", "LLM_SDK_MAX_RETRIES", "-1", "LLM_SDK_MAX_RETRIES: must be an integer between 0 and 5"},
		{"mock stream delay above max", "LLM_MOCK_STREAM_DELAY", "2s", "LLM_MOCK_STREAM_DELAY: must be greater than 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			env[tt.key] = tt.value

			_, err := Load(envFrom(env))
			if err == nil {
				t.Fatalf("Load() expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Load() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_LLM(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		wantProvider LLMProvider
	}{
		{"no key selects mock", nil, LLMProviderMock},
		{"key selects claude", map[string]string{"ANTHROPIC_API_KEY": "sk-test"}, LLMProviderClaude},
		{"explicit mock wins over key", map[string]string{"ANTHROPIC_API_KEY": "sk-test", "LLM_PROVIDER": "mock"}, LLMProviderMock},
		{"explicit claude with key", map[string]string{"ANTHROPIC_API_KEY": "sk-test", "LLM_PROVIDER": "claude"}, LLMProviderClaude},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			for k, v := range tt.env {
				env[k] = v
			}
			cfg, err := Load(envFrom(env))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.LLM.Provider != tt.wantProvider {
				t.Errorf("Provider = %q, want %q", cfg.LLM.Provider, tt.wantProvider)
			}
		})
	}

	cfg, err := Load(envFrom(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	want := LLMConfig{
		Provider: LLMProviderMock, Model: DefaultAnthropicModel,
		SegmentTimeout: DefaultLLMSegmentTimeout, DraftsTimeout: DefaultLLMDraftsTimeout,
		MaxAttempts: DefaultLLMMaxAttempts, SDKMaxRetries: DefaultLLMSDKMaxRetries,
		MockStreamDelay: DefaultLLMMockStreamDelay,
	}
	if cfg.LLM != want {
		t.Errorf("LLM defaults = %+v, want %+v", cfg.LLM, want)
	}

	env := validEnv()
	env["ANTHROPIC_MODEL"] = "claude-opus-5-5"
	env["LLM_SEGMENT_TIMEOUT"] = "5s"
	env["LLM_DRAFTS_TIMEOUT"] = "90s"
	env["LLM_MAX_ATTEMPTS"] = "3"
	env["LLM_SDK_MAX_RETRIES"] = "0"
	env["LLM_MOCK_STREAM_DELAY"] = "5ms"
	cfg, err = Load(envFrom(env))
	if err != nil {
		t.Fatal(err)
	}
	want = LLMConfig{
		Provider: LLMProviderMock, Model: "claude-opus-5-5",
		SegmentTimeout: 5 * time.Second, DraftsTimeout: 90 * time.Second,
		MaxAttempts: 3, SDKMaxRetries: 0, MockStreamDelay: 5 * time.Millisecond,
	}
	if cfg.LLM != want {
		t.Errorf("LLM overrides = %+v, want %+v", cfg.LLM, want)
	}
}

// TestLLMConfig_RouteTimeout: the HTTP route budget must cover every
// attempt so the service, not the router, decides when to give up.
func TestLLMConfig_RouteTimeout(t *testing.T) {
	c := LLMConfig{SegmentTimeout: 20 * time.Second, DraftsTimeout: 60 * time.Second, MaxAttempts: 2}
	if got, want := c.RouteTimeout(), 2*60*time.Second+llmRouteSlack; got != want {
		t.Errorf("RouteTimeout() = %v, want %v", got, want)
	}
}

func TestLoad_ReportsAllErrors(t *testing.T) {
	_, err := Load(envFrom(map[string]string{"LOG_LEVEL": "loud"}))
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	for _, want := range []string{"DATABASE_URL", "KAFKA_BROKERS", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestLoad_ErrorsNeverContainSecrets(t *testing.T) {
	// An invalid scheme still includes the password; it must not be echoed.
	env := validEnv()
	env["DATABASE_URL"] = "mysql://app:s3cr3t-pw@db:3306/x"
	env["ANTHROPIC_API_KEY"] = "sk-should-not-leak"
	env["LOG_LEVEL"] = "invalid"

	_, err := Load(envFrom(env))
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	for _, secret := range []string{"s3cr3t-pw", "sk-should-not-leak"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error message leaks secret %q: %q", secret, err)
		}
	}
}

func TestLoadMockProvider(t *testing.T) {
	cfg, err := LoadMockProvider(envFrom(nil))
	if err != nil {
		t.Fatalf("LoadMockProvider() unexpected error: %v", err)
	}
	if cfg.Addr != DefaultMockProviderAddr {
		t.Errorf("Addr = %q, want %q", cfg.Addr, DefaultMockProviderAddr)
	}

	_, err = LoadMockProvider(envFrom(map[string]string{"MOCK_PROVIDER_ADDR": "nope"}))
	if err == nil || !strings.Contains(err.Error(), "MOCK_PROVIDER_ADDR") {
		t.Errorf("LoadMockProvider() error = %v, want MOCK_PROVIDER_ADDR error", err)
	}
}

func TestLoad_Delivery(t *testing.T) {
	cfg, err := Load(envFrom(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Delivery
	if d.MaxAttempts != 5 || d.ProviderURL != DefaultProviderURL || d.RateLimitPerSecond != 90 || d.RateLimitBurst != 5 ||
		d.RetryBaseDelay != time.Second || d.RetryMaxDelay != time.Minute || d.RetryBackoffFactor != 2 ||
		d.Lease != time.Minute || d.TopicPartitions != 6 {
		t.Errorf("delivery defaults = %+v", d)
	}
	if d.RateLimitPerSecond+d.RateLimitBurst > MaxSendsPerSecond {
		t.Errorf("default rate + burst exceeds %d/s", MaxSendsPerSecond)
	}

	tests := []struct {
		name    string
		vars    map[string]string
		wantErr string
	}{
		{"rate plus burst over 100", map[string]string{"RATE_LIMIT_PER_SECOND": "96", "RATE_LIMIT_BURST": "5"}, "RATE_LIMIT_PER_SECOND"},
		{"rate over 100", map[string]string{"RATE_LIMIT_PER_SECOND": "101"}, "RATE_LIMIT_PER_SECOND"},
		{"zero burst", map[string]string{"RATE_LIMIT_BURST": "0"}, "RATE_LIMIT_BURST"},
		{"lease shorter than two provider timeouts", map[string]string{"DELIVERY_LEASE": "15s", "PROVIDER_TIMEOUT": "10s"}, "DELIVERY_LEASE"},
		{"max delay below base", map[string]string{"RETRY_BASE_DELAY": "10s", "RETRY_MAX_DELAY": "5s"}, "RETRY_MAX_DELAY"},
		{"too many attempts", map[string]string{"DELIVERY_MAX_ATTEMPTS": "21"}, "DELIVERY_MAX_ATTEMPTS"},
		{"provider URL without scheme", map[string]string{"PROVIDER_URL": "mock-provider:9090"}, "PROVIDER_URL"},
		{"provider URL with credentials", map[string]string{"PROVIDER_URL": "http://u:p@mock-provider:9090"}, "PROVIDER_URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := validEnv()
			maps.Copy(vars, tt.vars)
			_, err := Load(envFrom(vars))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Load() error = %v, want one mentioning %s", err, tt.wantErr)
			}
		})
	}

	vars := validEnv()
	vars["RATE_LIMIT_PER_SECOND"], vars["RATE_LIMIT_BURST"] = "95", "5"
	if _, err := Load(envFrom(vars)); err != nil {
		t.Errorf("95 + 5 rejected: %v", err)
	}
}

func TestLoadMockProvider_Faults(t *testing.T) {
	cfg, err := LoadMockProvider(envFrom(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxPerSecond != 100 || cfg.ServerErrorRate != DefaultMockServerErrorRate || cfg.Seed != 0 ||
		cfg.LostResponseDelay != DefaultMockLostResponseDelay {
		t.Errorf("defaults = %+v", cfg)
	}
	cfg, err = LoadMockProvider(envFrom(map[string]string{
		"MOCK_SEED": "7", "MOCK_SERVER_ERROR_RATE": "0", "MOCK_LATENCY_MIN": "0", "MOCK_LATENCY_MAX": "0s", "MOCK_MAX_PER_SECOND": "0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Seed != 7 || cfg.ServerErrorRate != 0 || cfg.LatencyMax != 0 || cfg.MaxPerSecond != 0 {
		t.Errorf("overrides = %+v", cfg)
	}
	for key, bad := range map[string]string{
		"MOCK_SERVER_ERROR_RATE": "1.5", "MOCK_RATE_LIMIT_RATE": "-0.1", "MOCK_LOST_RESPONSE_RATE": "often",
		"MOCK_LATENCY_MAX": "1ms", "MOCK_SEED": "-1",
	} {
		if _, err := LoadMockProvider(envFrom(map[string]string{key: bad})); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s=%s: error = %v", key, bad, err)
		}
	}
}

func TestLoadSeed(t *testing.T) {
	cfg, err := LoadSeed(envFrom(map[string]string{"DATABASE_URL": testDatabaseURL}))
	if err != nil {
		t.Fatalf("LoadSeed() unexpected error: %v", err)
	}
	if cfg.UserCount != DefaultSeedUserCount || cfg.RandomSeed != DefaultSeedRandomSeed {
		t.Errorf("defaults = %d users, seed %d", cfg.UserCount, cfg.RandomSeed)
	}

	cfg, err = LoadSeed(envFrom(map[string]string{
		"DATABASE_URL": testDatabaseURL, "SEED_USER_COUNT": "250", "SEED_RANDOM_SEED": "7",
	}))
	if err != nil || cfg.UserCount != 250 || cfg.RandomSeed != 7 {
		t.Errorf("LoadSeed() = %+v, %v; want 250 users, seed 7", cfg, err)
	}

	for _, bad := range []map[string]string{
		{"SEED_USER_COUNT": "0"},
		{"SEED_USER_COUNT": "100001"},
		{"SEED_USER_COUNT": "many"},
		{"SEED_RANDOM_SEED": "-1"},
	} {
		bad["DATABASE_URL"] = testDatabaseURL
		if _, err := LoadSeed(envFrom(bad)); err == nil {
			t.Errorf("LoadSeed(%v) = nil error", bad)
		}
	}
	if _, err := LoadSeed(envFrom(nil)); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("LoadSeed() without DATABASE_URL error = %v", err)
	}
}

func TestSecret_IsRedactedEverywhere(t *testing.T) {
	s := Secret("sk-very-secret")

	if got := fmt.Sprintf("%v %s %+v %#v", s, s, s, s); strings.Contains(got, "sk-very-secret") {
		t.Errorf("fmt output leaks secret: %q", got)
	}

	b, err := json.Marshal(struct{ Key Secret }{s})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(b), "sk-very-secret") {
		t.Errorf("JSON leaks secret: %s", b)
	}

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("cfg", "key", s, "cfg", Config{AnthropicAPIKey: s, DatabaseURL: s})
	if strings.Contains(buf.String(), "sk-very-secret") {
		t.Errorf("slog output leaks secret: %s", buf.String())
	}

	if s.Reveal() != "sk-very-secret" {
		t.Errorf("Reveal() = %q", s.Reveal())
	}
}
