---
type: Reference
title: Testing
description: What the Go and frontend test suites cover (failure scenarios, API validation, AI safety, fuzzing) and the CI pipeline.
tags: [testing, ci]
---

`make test` runs every Go test with `-race` (unit tests, plus
`integration`-tagged tests against throwaway Postgres and Kafka containers)
and the frontend tests. No `ANTHROPIC_API_KEY` is needed: AI tests use the
deterministic mock LLM or a stub HTTP server.

# Failure scenarios

`internal/delivery/scenario_integration_test.go` runs the approved example
campaign through the real claim, limiter, and provider client against an
in-process mock provider. A log in front of the provider is the ground
truth:

- 5xx bursts, 429 storms with `Retry-After`, client timeouts, "accepted but
  response lost", and permanent 4xx. Every delivery ends `sent` or
  `failed`, never more than 5 attempts. Each retry reaches the provider no
  earlier than its backoff and the previous `Retry-After`.
- Consent: no row without consent at approval, revoked consent fails
  unsent, and single-channel recipients get nothing on the other channel.
- Four worker instances, each with its own pool and limiter, under 429s:
  every one-second sliding window holds at most rate + burst requests per
  channel, retries included (20 + 5 in the test; 90 + 5 in the stack).
- Duplicates: a crash between provider acceptance and the database update,
  a lease that expires while the first worker is still sending, and (in
  `TestE2E_PipelineWithKafka`) every Kafka message and outbox row replayed.
  The provider holds at most one message per idempotency key and per
  recipient and channel.
- Graceful shutdown of every worker mid-campaign, then a restart: the
  campaign completes with no lost or duplicate message.
- Concurrent approvals are covered by
  `TestApprove_ConcurrentApprovalsMaterializeOnce` (Postgres).

# API validation

`internal/httpapi/validation_test.go`: malformed JSON, unknown fields,
wrong types, oversized bodies, wrong content types, invalid ids, wrong
states, and stale versions on every campaign endpoint. Error bodies are
checked for SQL, driver errors, and stack traces.

# AI safety

The prompt-injection corpus and hostile model outputs (extra `approve`
fields, raw SQL, `{{phone}}`, HTML, overlong SMS) go through the HTTP
endpoints. Results are a validated proposal or a safe error, and nothing
gets approved or queued.

# Fuzzing

Go native fuzz targets for the segment rule validator and compiler, the
template parser and per-recipient renderer, and the LLM output decoders.
`make test` runs their seed corpora; `make fuzz` fuzzes.

# CI

GitHub Actions (`.github/workflows/ci.yml`) runs on pushes to `master`,
pull requests, and manual dispatch. Its jobs run in parallel, and each one
calls a Makefile target, so a failure can be reproduced locally:

- **Format check**: `make fmt-check` (gofmt + goimports).
- **Lint**: `make lint`. That is golangci-lint with go vet, staticcheck,
  gosec, errcheck and the other linters in `.golangci.yml`, plus ESLint and
  vue-tsc type checking.
- **Static analysis**: `make vuln` (govulncheck on reachable Go code).
- **Tests**: `make test` on `ubuntu-latest`, with Go from the `go.mod`
  toolchain and the same Node version as the frontend image. The runner's
  Docker hosts the testcontainers.
- **Build image**: builds each image from `docker-compose.yml` (api,
  worker, seed, mock-provider, frontend) with Buildx and the GitHub Actions
  cache.

Nothing is pushed or deployed, and no secrets are used.

Mapping of requirements to tests: [Requirements coverage](../references/requirements-coverage.md).
