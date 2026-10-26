---
type: Reference
title: Architecture
description: The running services, the Go package layout, and the rule that dependencies flow inward toward the domain packages.
tags: [architecture]
---

# Services

```
browser ──► frontend (nginx) ──/api──► api ──► postgres ◄──────────┐
                                                (approval writes      │ claims, rate limit,
                                                 deliveries + outbox) │ outcomes
                                     kafka ◄── outbox relay ── worker(s) ──► mock-provider
   migrate (one-shot) ──► seed (one-shot) ──► postgres
```

# Packages

```
cmd/api            API entry point (chi router, /healthz, /readyz, /metrics, /api/v1)
cmd/worker         worker entry point (outbox relay, consumer, maintenance, probes, metrics)
cmd/seed           one-shot fake-data loader
cmd/smoke          end-to-end check against the running stack (make smoke)
mock-provider/     mock SMS/email provider entry point
internal/
  config           env loading + validation, redacting Secret type
  health           liveness/readiness handlers, concurrent checks
  segment          rule DSL: strict parser/validator, SQL compiler, preview service
  campaign         state machine, edits, generation lease, preview, approval, scheduling
                   and release (Service);
                   AI generation: brief validation, output decoding, repair loop (Generator)
  campaign/campaigntest  in-memory Store and AudienceInspector fakes for unit tests
  content          drafts, {{variable}} templates, channel limits, plain-text checks,
                   per-recipient rendering with final validation
  delivery         claim → rate limit → consent/render → send → record (Processor);
                   retry policy, shared-bucket limiter, outbox relay, maintenance loops
                   (including the release of due scheduled campaigns)
  monitoring       progress read model: snapshot, shared per-campaign poller (Hub)
  mockprovider     the mock provider's API: idempotency, fake-recipient guard, faults
  llm              LLM port (interface, request types, errors)
  llm/mockllm      deterministic keyword mock with scripted test faults
  strictjson       strict JSON decoding (duplicate keys, unknown fields, trailing data)
  seed             deterministic fake-data generator + transactional loader
  httpapi          /api/v1 handlers (segment preview, campaigns, SSE draft stream,
                   progress JSON + SSE)
  worker           worker lifecycle: dependency wait, setup, long-running tasks
  platform/
    httpserver     server timeouts, middleware, graceful shutdown, probe router + client
    metrics        Prometheus registry, HTTP middleware, delivery and LLM adapters
    logging        slog JSON logger
    postgres       pgx pool, health checker, audience repository (consent filter),
                   campaign store (row lock + version check, audit events, delivery
                   materialization), delivery store (claim/lease, outbox), token bucket,
                   progress repository (one aggregate query)
    kafka          franz-go client, health checker, topic setup, publisher, consumer group
    provider       HTTP client for the mock provider (error classification, Retry-After)
    claude         Claude API adapter (anthropic-sdk-go, structured outputs, streaming)
  testutil/pgtest  testcontainers Postgres + migrations (integration tag)
  testutil/kafkatest  testcontainers Kafka (apache/kafka, KRaft; integration tag)
migrations/        versioned SQL (golang-migrate)
frontend/          Vue 3 + TypeScript (strict) + Vite + vue-router, Vitest, ESLint
  src/api          typed client, runtime response guards, fetch-based SSE reader
  src/views        campaign list, new campaign, review (stream, edit, preview, approve),
                   delivery dashboard (EventSource)
docs/              this reference bundle
```

# Dependency direction

Dependencies flow inward. Entry points wire the platform adapters. The
`segment`, `content`, `llm`, `campaign`, and `delivery` packages import
neither HTTP, database, queue, nor LLM SDK code. The Postgres and Kafka
adapters, the provider and Claude clients, the metrics adapters, and the
HTTP handlers depend on them, not the reverse. `delivery` and `campaign`
report metrics through small observer ports (`delivery.Metrics`,
`campaign.LLMMetrics`); only `platform/metrics` imports Prometheus.

The reasoning behind each choice is in the
[technical decisions](../../ai-sessions/DECISIONS.md).
