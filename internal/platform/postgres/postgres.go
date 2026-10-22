// Package postgres owns the PostgreSQL connection pool.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool settings sized for a single local instance; revisit with real load.
const (
	maxConns          = 10
	maxConnIdleTime   = 5 * time.Minute
	healthCheckPeriod = 30 * time.Second
)

// errInvalidURL is returned instead of the pgx parse error, which can quote
// the connection string including its password.
var errInvalidURL = errors.New("invalid database URL")

// NewPool parses databaseURL and returns a pool. Connections are opened
// lazily, so a pool is returned even while the database is down; readiness
// checks surface that state instead of crashing the process.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errInvalidURL
	}
	cfg.MaxConns = maxConns
	cfg.MaxConnIdleTime = maxConnIdleTime
	cfg.HealthCheckPeriod = healthCheckPeriod

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	return pool, nil
}

// Checker reports whether the database accepts connections.
type Checker struct {
	pool *pgxpool.Pool
}

// NewChecker returns a health checker backed by pool.
func NewChecker(pool *pgxpool.Pool) Checker { return Checker{pool: pool} }

// Check pings the database.
func (c Checker) Check(ctx context.Context) error {
	if err := c.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}
	return nil
}
