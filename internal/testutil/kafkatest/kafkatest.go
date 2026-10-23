//go:build integration

// Package kafkatest starts a disposable single-node Kafka (KRaft) for
// integration tests, using the same apache/kafka image as
// docker-compose.yml. Docker is required.
package kafkatest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Image matches the kafka service in docker-compose.yml.
const Image = "apache/kafka:4.3.2"

const (
	externalPort = "9093/tcp"
	starter      = "/tmp/start.sh"
)

// Start runs Kafka until the test ends and returns its bootstrap address.
// The advertised listener must name the host-mapped port, which is known
// only after the container starts, so the entrypoint waits for a starter
// script that a post-start hook writes.
func Start(t *testing.T) []string {
	t.Helper()
	ctx := t.Context()
	ctr, err := testcontainers.Run(ctx, Image,
		testcontainers.WithExposedPorts(externalPort),
		testcontainers.WithEnv(map[string]string{
			"KAFKA_NODE_ID":                                  "1",
			"KAFKA_PROCESS_ROLES":                            "broker,controller",
			"KAFKA_LISTENERS":                                "BROKER://:9092,PLAINTEXT://:9093,CONTROLLER://:9094",
			"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP":           "BROKER:PLAINTEXT,PLAINTEXT:PLAINTEXT,CONTROLLER:PLAINTEXT",
			"KAFKA_INTER_BROKER_LISTENER_NAME":               "BROKER",
			"KAFKA_CONTROLLER_LISTENER_NAMES":                "CONTROLLER",
			"KAFKA_CONTROLLER_QUORUM_VOTERS":                 "1@localhost:9094",
			"KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR":         "1",
			"KAFKA_OFFSETS_TOPIC_NUM_PARTITIONS":             "1",
			"KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR": "1",
			"KAFKA_TRANSACTION_STATE_LOG_MIN_ISR":            "1",
			"KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS":         "0",
			"KAFKA_AUTO_CREATE_TOPICS_ENABLE":                "false",
		}),
		testcontainers.WithEntrypoint("sh"),
		testcontainers.WithCmd("-c", "while [ ! -f "+starter+" ]; do sleep 0.1; done; exec bash "+starter),
		testcontainers.WithLifecycleHooks(testcontainers.ContainerLifecycleHooks{
			PostStarts: []testcontainers.ContainerHook{func(ctx context.Context, c testcontainers.Container) error {
				if err := wait.ForMappedPort(externalPort).WaitUntilReady(ctx, c); err != nil {
					return fmt.Errorf("wait for mapped port: %w", err)
				}
				endpoint, err := c.PortEndpoint(ctx, externalPort, "")
				if err != nil {
					return fmt.Errorf("port endpoint: %w", err)
				}
				inspect, err := c.Inspect(ctx)
				if err != nil {
					return fmt.Errorf("inspect: %w", err)
				}
				script := fmt.Sprintf("#!/bin/bash\nexport KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://%s,BROKER://%s:9092\nexec /etc/kafka/docker/run\n",
					endpoint, inspect.Config.Hostname)
				if err := c.CopyToContainer(ctx, []byte(script), starter, 0o755); err != nil {
					return fmt.Errorf("copy starter script: %w", err)
				}
				return wait.ForLog("Kafka Server started").WithStartupTimeout(90*time.Second).WaitUntilReady(ctx, c)
			}},
		}),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start kafka container: %v", err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, externalPort, "")
	if err != nil {
		t.Fatalf("kafka endpoint: %v", err)
	}
	return []string{endpoint}
}
