//go:build integration

// Package pgtest starts a disposable PostgreSQL container for integration
// tests and applies the repository's migrations to it. Docker is required.
package pgtest

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// driver
	_ "github.com/golang-migrate/migrate/v4/source/file"     // registers the file:// source
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Image matches the postgres service in docker-compose.yml.
const Image = "postgres:18.6-alpine"

// Start runs a PostgreSQL container that is terminated when the test ends
// and returns its connection string.
func Start(t *testing.T) string {
	t.Helper()
	ctx := t.Context()

	ctr, err := tcpostgres.Run(ctx, Image,
		tcpostgres.WithDatabase("campaigns_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	return dsn
}

// Migrate applies every up migration in migrations/ to the database at dsn
// and returns the resulting schema version.
func Migrate(t *testing.T, dsn string) uint {
	t.Helper()
	m := newMigrate(t, dsn)
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("apply migrations: %v", err)
	}
	version, dirty, err := m.Version()
	if err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if dirty {
		t.Fatalf("migration version %d is dirty", version)
	}
	return version
}

// MigrateDown applies every down migration, leaving an empty schema.
func MigrateDown(t *testing.T, dsn string) {
	t.Helper()
	if err := newMigrate(t, dsn).Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("revert migrations: %v", err)
	}
}

func newMigrate(t *testing.T, dsn string) *migrate.Migrate {
	t.Helper()
	// golang-migrate selects its pgx v5 driver by the pgx5:// scheme.
	migrateURL, ok := strings.CutPrefix(dsn, "postgres://")
	if !ok {
		t.Fatalf("unexpected DSN scheme from testcontainers")
	}
	m, err := migrate.New("file://"+MigrationsDir(t), "pgx5://"+migrateURL)
	if err != nil {
		t.Fatalf("init migrate: %v", err)
	}
	t.Cleanup(func() { _, _ = m.Close() })
	return m
}

// MigrationsDir returns the absolute path of the repository's migrations/.
func MigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve pgtest source location")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "migrations")
}
