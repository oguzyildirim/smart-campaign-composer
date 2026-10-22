package postgres

import (
	"strings"
	"testing"
	"time"
)

func TestNewPool_InvalidURLDoesNotLeakPassword(t *testing.T) {
	_, err := NewPool(t.Context(), "postgres://app:s3cr3t-pw@localhost:5432/db?sslmode=bogus")
	if err == nil {
		t.Fatal("NewPool() with invalid sslmode returned nil error")
	}
	if strings.Contains(err.Error(), "s3cr3t-pw") {
		t.Errorf("error leaks password: %q", err)
	}
}

func TestChecker_FailsWhenDatabaseIsDown(t *testing.T) {
	// Nothing listens on port 1; the lazy pool fails on first use.
	pool, err := NewPool(t.Context(), "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("NewPool() = %v; the pool must be created even when the database is down", err)
	}
	t.Cleanup(pool.Close)

	start := time.Now()
	if err := NewChecker(pool).Check(t.Context()); err == nil {
		t.Fatal("Check() = nil against an unreachable database")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Check() took %v against a refused connection", elapsed)
	}
}
