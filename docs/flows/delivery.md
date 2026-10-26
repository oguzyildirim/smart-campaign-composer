---
type: Flow
title: Delivery
description: How approved campaigns are delivered through the outbox, Kafka, and workers with idempotency, retries, a shared rate limit, and crash recovery.
tags: [delivery, kafka, reliability]
---

# Pipeline

```
approve ──(one transaction)──► campaigns.status = approved
                               deliveries: one row per consented (user, channel), status queued
                               outbox: one job message per delivery
worker: outbox relay ──► Kafka deliveries.sms / deliveries.email (acks=all)
worker: consumer ──► claim row (lease) ──► rate-limit token ──► consent + render + validate
                 ──► POST mock-provider (Idempotency-Key = delivery id) ──► record outcome
                 ──► commit offset
worker: maintenance (every 1 s) ──► due scheduled campaigns → released (deliveries + outbox),
                                    release_failed, or expired;
                                    expired leases → retrying; due retries → queued + outbox;
                                    campaigns with only sent/failed deliveries → completed
```

# Guarantees

- **One delivery per person, channel, and campaign.** `deliveries` has
  `UNIQUE (campaign_id, user_id, channel)`. Approving again creates nothing.
- **Duplicates are absorbed.** A Kafka message only names a delivery. A
  worker must claim the row (`queued`/due `retrying` → `processing`, with a
  lease) before sending, so a redelivered message for a row that is
  processing, sent, or failed is skipped. The attempt counter fences the
  outcome: a worker whose lease expired cannot overwrite a newer attempt.
- **Uncertain outcomes are retried with the same key.** After a timeout the
  provider may already have the message; the retry sends the same
  `Idempotency-Key` (the delivery id), and the
  [provider](../api/mock-provider.md) returns the original result instead
  of delivering twice.
- **Consent and content are checked right before sending.** Consent is read
  from the database after the rate-limit wait. Revoked consent fails the
  delivery with `consent_revoked` without calling the provider. The message
  is rendered for the recipient and re-validated (SMS ≤ 160 characters,
  subject ≤ 80, no leftover `{{…}}`); a failure is `invalid_content`.

# Retries

Network errors, timeouts, 408, 429, and 5xx are transient; other 4xx are
permanent. Transient failures wait an exponential backoff with full jitter
(1 s · 2^(attempt−1), capped at 60 s), or the provider's `Retry-After` when
longer (also capped). After 5 attempts the delivery fails with
`retries_exhausted`. Retries go back through the outbox, the claim, and the
rate limiter, like first attempts.

# Rate limit

One token bucket per channel in Postgres, shared by every worker and
refilled by the database clock: 90 tokens/s with a burst of 5, so any
one-second window holds at most 95 sends per channel. Rate + burst may not
exceed 100 (enforced at startup).

# Crash recovery

A killed worker leaves its claimed rows in `processing`; when their lease
(60 s) expires, maintenance moves them to `retrying` (the attempt stays
counted). Uncommitted Kafka messages are redelivered to the other workers.
On SIGTERM a worker stops polling, gives in-flight sends half of
`SHUTDOWN_TIMEOUT`, and commits offsets only for batches that finished.

# Statuses and error codes

Delivery statuses: `queued`, `processing`, `retrying`, `sent`, `failed`.

Error codes in `last_error_code`: `consent_revoked`, `invalid_content`,
`provider_rejected`, `provider_rate_limited`, `provider_unavailable`,
`provider_timeout`, `provider_invalid_response`, `interrupted`,
`internal_error`, `lease_expired`, `retries_exhausted`.

# Examples

Run several workers and watch a campaign:

```bash
docker compose up -d --scale worker=3
docker compose exec postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c \
  "SELECT channel, status, count(*) FROM deliveries GROUP BY 1, 2 ORDER BY 1, 2"'
curl -s localhost:9090/v1/stats | jq .channels
```

Related: [Progress](../api/progress.md), [Known limitations](../references/known-limitations.md).
