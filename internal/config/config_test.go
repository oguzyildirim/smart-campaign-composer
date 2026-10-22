package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
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
