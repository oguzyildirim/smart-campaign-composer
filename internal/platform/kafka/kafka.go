// Package kafka owns the Kafka client used by the API and the worker.
package kafka

import (
	"context"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
)

// NewClient returns a franz-go client for brokers. The client connects
// lazily, so it is returned even while Kafka is down.
func NewClient(brokers []string, opts ...kgo.Opt) (*kgo.Client, error) {
	all := append([]kgo.Opt{kgo.SeedBrokers(brokers...)}, opts...)
	client, err := kgo.NewClient(all...)
	if err != nil {
		return nil, fmt.Errorf("create kafka client: %w", err)
	}
	return client, nil
}

// Checker reports whether at least one broker answers.
type Checker struct {
	client *kgo.Client
}

// NewChecker returns a health checker backed by client.
func NewChecker(client *kgo.Client) Checker { return Checker{client: client} }

// Check pings the cluster.
func (c Checker) Check(ctx context.Context) error {
	if err := c.client.Ping(ctx); err != nil {
		return fmt.Errorf("ping kafka: %w", err)
	}
	return nil
}
